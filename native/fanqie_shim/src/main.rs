//! 独立可执行的番茄内容引擎。
//!
//! 桌面端（Windows / macOS / Linux）与「本地服务版」用它代替进程内 FFI：
//! 引擎在 127.0.0.1:<port> 起一个 loopback HTTP 服务，宿主（Flutter 应用或
//! Go 写的本地服务端）只按 HTTP 调它。这么做的好处：
//!
//! * 不依赖动态库查找路径，也不牵扯 macOS / iOS 的代码签名；
//! * 同一个二进制既能给桌面 App 用，也能给本地服务版用；
//! * 引擎崩了只死自己，不会把宿主一起带走。
//!
//! 用法：
//! ```text
//! fanqie_engine --config <config.json> --pool <device_pool.json> \
//!               --filter <filter.json> --runtime <dir> [--port 18921] \
//!               [--exit-with-parent]
//! ```
//!
//! `--exit-with-parent` 会盯住 stdin：宿主把管道写端一关（进程退出），
//! 引擎立刻跟着退出，避免留下孤儿进程占着端口。

use std::io::{Read, Write};
use std::process::ExitCode;

struct Options {
    config: String,
    pool: String,
    filter: String,
    runtime: String,
    port: u16,
    exit_with_parent: bool,
}

fn usage() -> &'static str {
    "fanqie_engine --config <file> --pool <file> --filter <file> --runtime <dir> \
[--port <n>] [--exit-with-parent]"
}

fn take_value(args: &[String], index: &mut usize, flag: &str) -> Result<String, String> {
    *index += 1;
    args.get(*index)
        .cloned()
        .ok_or_else(|| format!("{flag} 缺少取值"))
}

fn parse_args(args: Vec<String>) -> Result<Options, String> {
    let mut options = Options {
        config: String::new(),
        pool: String::new(),
        filter: String::new(),
        runtime: String::new(),
        port: 18921,
        exit_with_parent: false,
    };
    let mut index = 0usize;
    while index < args.len() {
        match args[index].as_str() {
            "--config" => options.config = take_value(&args, &mut index, "--config")?,
            "--pool" => options.pool = take_value(&args, &mut index, "--pool")?,
            "--filter" => options.filter = take_value(&args, &mut index, "--filter")?,
            "--runtime" => options.runtime = take_value(&args, &mut index, "--runtime")?,
            "--port" => {
                let raw = take_value(&args, &mut index, "--port")?;
                options.port = raw
                    .parse::<u16>()
                    .map_err(|_| format!("--port 不是合法端口：{raw}"))?;
            }
            "--exit-with-parent" => options.exit_with_parent = true,
            "-h" | "--help" => {
                println!("{}", usage());
                std::process::exit(0);
            }
            other => return Err(format!("未知参数：{other}")),
        }
        index += 1;
    }
    // 环境变量兜底：宿主不方便拼参数时可以直接用 env。
    if options.config.is_empty() {
        options.config = std::env::var("FANQIE_CONFIG").unwrap_or_default();
    }
    if options.pool.is_empty() {
        options.pool = std::env::var("FANQIE_POOL").unwrap_or_default();
    }
    if options.filter.is_empty() {
        options.filter = std::env::var("FANQIE_FILTER").unwrap_or_default();
    }
    if options.runtime.is_empty() {
        options.runtime = std::env::var("FANQIE_RUNTIME").unwrap_or_default();
    }
    if options.config.is_empty() || options.runtime.is_empty() {
        return Err(format!("缺少必填参数。\n{}", usage()));
    }
    // pool / filter 允许为空串（引擎会自行按默认路径处理），但目录必须存在。
    if options.pool.is_empty() {
        options.pool = format!("{}/device_pool.json", options.runtime);
    }
    if options.filter.is_empty() {
        options.filter = format!("{}/filter.json", options.runtime);
    }
    Ok(options)
}

fn main() -> ExitCode {
    let options = match parse_args(std::env::args().skip(1).collect()) {
        Ok(value) => value,
        Err(message) => {
            eprintln!("fanqie_engine: {message}");
            return ExitCode::from(2);
        }
    };
    let _ = std::fs::create_dir_all(&options.runtime);

    let runtime = match tokio::runtime::Builder::new_multi_thread()
        .enable_all()
        .build()
    {
        Ok(value) => value,
        Err(error) => {
            eprintln!("fanqie_engine: 无法创建 tokio runtime: {error}");
            return ExitCode::FAILURE;
        }
    };

    let result = runtime.block_on(fqapi_core::api::init(
        options.config.clone(),
        options.pool.clone(),
        options.filter.clone(),
        options.runtime.clone(),
        options.port,
        false,
        String::new(),
    ));
    let status = match result {
        Ok(value) => value,
        Err(error) => {
            eprintln!("fanqie_engine: init 失败: {error}");
            return ExitCode::FAILURE;
        }
    };
    // 宿主靠这行判断「引擎已就绪」，改文案要同步 lib/fanqie_backend.dart。
    println!("fanqie-engine-ready {status} port={}", options.port);
    let _ = std::io::stdout().flush();

    if options.exit_with_parent {
        // 宿主退出 → 我们的 stdin 读到 EOF → 立刻退出，避免孤儿进程占端口。
        std::thread::spawn(|| {
            let mut sink = [0u8; 64];
            let mut stdin = std::io::stdin();
            loop {
                match stdin.read(&mut sink) {
                    Ok(0) => std::process::exit(0),
                    Ok(_) => continue,
                    Err(_) => std::process::exit(0),
                }
            }
        });
    }

    // runtime 必须活到进程结束，否则 tokio 任务会被丢弃。
    loop {
        std::thread::park();
    }
}
