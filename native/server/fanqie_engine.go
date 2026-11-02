package main

// 番茄内容引擎的拉起逻辑（本地服务版）。
//
// 番茄站源不是普通 HTTP 抓取：上游的签名、设备注册和视频缓存都在一个
// 独立的 Rust 核心里（GPL-3.0 的 ch6vip/fqapp）。Android / iOS 把这个核心
// 链进宿主进程，桌面端与本地服务版则用同源编出的独立可执行
// `fanqie_engine`，由宿主拉起后走 127.0.0.1:18921 的 loopback REST。
//
// 这里只做三件事：写配置、拉起引擎、把进程的生命周期绑在服务进程上。
// 找不到引擎不算错误——番茄源照样列出来，播放时会给出明确提示，其余站源
// 完全不受影响。

import (
	"embed"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"
)

// 引擎要求 config / filter 两个 JSON 存在，内容与 Flutter 端的
// assets/fanqie/ 完全一致；改动其中任意一份都要同步另一份。
//
//go:embed fanqie/config.json fanqie/filter.json
var fanqieAssets embed.FS

const fanqieEnginePort = 18921

// startFanqieEngine 在后台拉起番茄引擎，返回的清理函数会在服务退出时调用。
// 任何失败都只写日志，不阻断本地服务的启动。
func startFanqieEngine(dataDirectory string) func() {
	cleanup := func() {}
	executable, err := os.Executable()
	if err != nil {
		log.Printf("番茄引擎：无法定位服务程序路径：%v", err)
		return cleanup
	}
	enginePath := fanqieEnginePath(filepath.Dir(executable))
	if enginePath == "" {
		// 没有引擎产物是正常情况（比如只想要其它站源），不做任何提示打扰用户。
		return cleanup
	}
	if fanqiePortOpen() {
		log.Printf("番茄引擎：复用已在运行的服务（127.0.0.1:%d）", fanqieEnginePort)
		return cleanup
	}

	runtimeDirectory := filepath.Join(dataDirectory, "fanqie")
	if err := os.MkdirAll(runtimeDirectory, 0o755); err != nil {
		log.Printf("番茄引擎：无法创建运行目录 %s：%v", runtimeDirectory, err)
		return cleanup
	}
	for _, item := range []struct{ source, target string }{
		{"fanqie/config.json", "config.json"},
		{"fanqie/filter.json", "filter.json"},
	} {
		payload, err := fanqieAssets.ReadFile(item.source)
		if err != nil {
			log.Printf("番茄引擎：内置配置缺失（%s）：%v", item.source, err)
			return cleanup
		}
		if err := os.WriteFile(filepath.Join(runtimeDirectory, item.target), payload, 0o644); err != nil {
			log.Printf("番茄引擎：写入配置失败：%v", err)
			return cleanup
		}
	}

	command := exec.Command(enginePath,
		"--config", filepath.Join(runtimeDirectory, "config.json"),
		"--pool", filepath.Join(runtimeDirectory, "device_pool.json"),
		"--filter", filepath.Join(runtimeDirectory, "filter.json"),
		"--runtime", runtimeDirectory,
		"--port", fmt.Sprint(fanqieEnginePort),
		// 我们把 stdin 管道留在手里：服务进程一退出，引擎读到 EOF 自己收工，
		// 不会留下孤儿进程占着 18921。
		"--exit-with-parent",
	)
	stdin, err := command.StdinPipe()
	if err != nil {
		log.Printf("番茄引擎：无法建立管道：%v", err)
		return cleanup
	}
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	if err := command.Start(); err != nil {
		log.Printf("番茄引擎：启动失败：%v", err)
		return cleanup
	}
	go func() {
		// 引擎起不来时把原因留在日志里，别让用户只看到「未就绪」。
		deadline := time.Now().Add(20 * time.Second)
		for time.Now().Before(deadline) {
			if fanqiePortOpen() {
				log.Printf("番茄引擎：就绪（127.0.0.1:%d）", fanqieEnginePort)
				return
			}
			time.Sleep(250 * time.Millisecond)
		}
		log.Printf("番茄引擎：20 秒内未就绪，番茄站源会提示引擎未就绪")
	}()
	return func() {
		// 先关 stdin 让引擎自行退出，再兜底 kill，最后回收进程避免僵尸。
		_ = stdin.Close()
		done := make(chan struct{})
		go func() {
			_ = command.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			_ = command.Process.Kill()
			<-done
		}
	}
}

// fanqieEnginePath 找出与当前服务程序同目录的引擎可执行文件。
//
// 两种命名都要认：
//   - 应用包内是固定名 `fanqie_engine`（Windows 加 .exe）；
//   - 本地服务版的 zip 里四个平台的二进制平铺在一起，引擎必须带平台后缀
//     （fanqie_engine-windows.exe / -linux / -macos-arm / -macos-intel），
//     否则同名文件会互相覆盖。
func fanqieEnginePath(directory string) string {
	plain := filepath.Join(directory, "fanqie_engine")
	if runtime.GOOS == "windows" {
		plain += ".exe"
	}
	var flavoured []string
	switch runtime.GOOS {
	case "windows":
		flavoured = []string{filepath.Join(directory, "fanqie_engine-windows.exe")}
	case "darwin":
		arch := "arm"
		if runtime.GOARCH == "amd64" {
			arch = "intel"
		}
		flavoured = []string{filepath.Join(directory, "fanqie_engine-macos-"+arch)}
	default:
		flavoured = []string{filepath.Join(directory, "fanqie_engine-"+runtime.GOOS)}
	}
	for _, candidate := range append(flavoured, plain) {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate
		}
	}
	return ""
}

// fanqiePortOpen 探测引擎端口是否已经在监听。
func fanqiePortOpen() bool {
	connection, err := net.DialTimeout("tcp",
		fmt.Sprintf("127.0.0.1:%d", fanqieEnginePort), 300*time.Millisecond)
	if err != nil {
		return false
	}
	_ = connection.Close()
	return true
}
