Pod::Spec.new do |spec|
  spec.name = 'FanqieEngine'
  spec.version = '0.1.0'
  spec.summary = '番茄内容引擎（Rust 核心静态库）'
  spec.description = <<-DESC
番茄短剧上游的签名、设备注册与视频缓存都在一个 Rust 核心（上游 GPL-3.0 的
ch6vip/fqapp rust/ 目录）里。iOS 不允许 load App 包以外的动态库，也无法
fork 子进程，所以这里把该核心编成静态库链进 Runner，Dart 侧用
DynamicLibrary.process() 找 fanqie_init / fanqie_status 两个 C 符号。
  DESC
  spec.homepage = 'https://github.com/ch6vip/fqapp'
  spec.author = '真果鉴 contributors'
  spec.source = { :path => '.' }
  spec.license = { :type => 'GPL-3.0' }
  spec.ios.deployment_target = '15.1'

  # 静态库由 scripts/build_fanqie_engine.py --target aarch64-apple-ios 编出。
  # 没有产物时（比如本地没跑过该脚本）只跳过链接，不阻断 pod install——
  # 应用照常能装，番茄源显示但播放时提示引擎未就绪。
  library = File.join(__dir__, 'libfanqie_shim.a')
  available = File.exist?(library)

  if available
    spec.vendored_libraries = 'libfanqie_shim.a'
    # -u 强制把这两个符号拉进来：静态库里的目标文件没有别的引用，
    # 不加 -u 会被链接器整段丢掉，运行时 DynamicLibrary.process() 就找不到。
    spec.user_target_xcconfig = {
      'OTHER_LDFLAGS' => '$(inherited) -Wl,-u,_fanqie_init -Wl,-u,_fanqie_status -Wl,-export_dynamic',
      'STRIP_STYLE' => 'non-global'
    }
  else
    spec.user_target_xcconfig = {
      'OTHER_LDFLAGS' => '$(inherited)',
      'STRIP_STYLE' => 'non-global'
    }
  end
end
