cmake_minimum_required(VERSION 3.14)

option(MEDIA_KIT_LIBS_AVAILABLE "package:media_kit libraries are available." ON)
set(PROJECT_NAME "media_kit_libs_windows_video")
project(${PROJECT_NAME} LANGUAGES CXX)
add_compile_definitions(_DISABLE_CONSTEXPR_MUTEX_CONSTRUCTOR)

if(NOT CMAKE_SIZEOF_VOID_P EQUAL 8 OR FLUTTER_TARGET_PLATFORM STREQUAL "windows-arm64")
  message(FATAL_ERROR "The pinned media libraries require Windows x64.")
endif()

# duanju_native_archive(<文件名> <本地缓存路径> <URL> <sha256> <解包目录> [必需文件...])
#
# 为什么不再「一次 file(DOWNLOAD) 定生死」：Windows CI 上这一步抓的是 GitHub
# 的 release 资源（会 302 到 release-assets.githubusercontent.com），该域名偶发
# 403/429/连接重置，一次失败就让整个 flutter build 停在「Unable to generate
# build files」，而且错误只说 “HTTP response code said error”，看不出是网络还是
# 资源没了。现在分三层：
#   1. 本地缓存优先：CI 预下载好的包（DUANJU_MPV_ARCHIVE / DUANJU_ANGLE_ARCHIVE）
#      直接拷进来，仍然逐字节校验 sha256，不走网络；
#   2. 镜像优先于官方：设了 DUANJU_GITHUB_MIRROR 就先走镜像前缀；
#   3. 每个地址重试 3 次，全部失败才报错，并把最后一次的真实原因带出来。
function(duanju_native_archive filename local_override url digest destination)
  set(archive "${CMAKE_BINARY_DIR}/${filename}")

  # 已下载过：哈希对得上就复用，对不上说明是半截文件，删掉重来。
  if(EXISTS "${archive}")
    file(SHA256 "${archive}" actual_digest)
    if(NOT actual_digest STREQUAL digest)
      file(REMOVE "${archive}")
    endif()
  endif()

  if(NOT EXISTS "${archive}" AND local_override AND EXISTS "${local_override}")
    file(SHA256 "${local_override}" override_digest)
    if(override_digest STREQUAL digest)
      message(STATUS "Using cached ${filename}: ${local_override}")
      configure_file("${local_override}" "${archive}" COPYONLY)
    else()
      message(WARNING "本地缓存 ${local_override} 的 sha256 与预期不符，改为联网下载 ${filename}")
    endif()
  endif()

  if(NOT EXISTS "${archive}")
    set(candidates "")
    if(DEFINED ENV{DUANJU_GITHUB_MIRROR} AND NOT "$ENV{DUANJU_GITHUB_MIRROR}" STREQUAL "")
      # 允许写成 https://ghproxy.net 或 https://ghproxy.net/，统一去掉尾部斜杠。
      string(REGEX REPLACE "/+$" "" mirror "$ENV{DUANJU_GITHUB_MIRROR}")
      list(APPEND candidates "${mirror}/${url}")
    endif()
    list(APPEND candidates "${url}")

    set(downloaded FALSE)
    set(last_status "没有可用的下载地址")
    foreach(candidate IN LISTS candidates)
      foreach(attempt RANGE 1 3)
        message(STATUS "Downloading ${filename} (try ${attempt}) from ${candidate}")
        # 这里绝不能带 EXPECTED_HASH：CMake 在下载失败时会先试算哈希，直接抛
        # 「file DOWNLOAD cannot compute hash on failed download」，把整个 configure
        # 打断——既不会执行我们的 STATUS 判断，也没机会重试。哈希自己校验。
        file(DOWNLOAD "${candidate}" "${archive}"
          TLS_VERIFY ON
          # 比旧值宽松：release 资源经 CDN 跳转后限速，45 秒无数据就断太容易误杀。
          TIMEOUT 600
          INACTIVITY_TIMEOUT 120
          STATUS download_status
        )
        list(GET download_status 0 download_code)
        if(download_code EQUAL 0 AND EXISTS "${archive}")
          file(SHA256 "${archive}" fetched_digest)
          if(fetched_digest STREQUAL digest)
            set(downloaded TRUE)
            break()
          endif()
          set(last_status "sha256 不符（期望 ${digest}，实际 ${fetched_digest}）(${candidate})")
        else()
          list(GET download_status 1 download_reason)
          set(last_status "${download_code}: ${download_reason} (${candidate})")
        endif()
        message(STATUS "下载失败：${last_status}")
        file(REMOVE "${archive}")
      endforeach()
      if(downloaded)
        break()
      endif()
    endforeach()

    if(NOT downloaded)
      message(FATAL_ERROR "Failed to download ${filename}: ${last_status}")
    endif()
  endif()

  set(valid FALSE)
  if(EXISTS "${destination}/.duanju-sha256")
    file(READ "${destination}/.duanju-sha256" extracted_digest)
    if(extracted_digest STREQUAL digest)
      set(valid TRUE)
    endif()
  endif()
  foreach(required IN LISTS ARGN)
    if(NOT EXISTS "${destination}/${required}")
      set(valid FALSE)
    endif()
  endforeach()
  if(NOT valid)
    file(REMOVE_RECURSE "${destination}")
    file(MAKE_DIRECTORY "${destination}")
    execute_process(
      COMMAND "${CMAKE_COMMAND}" -E tar xf "${archive}"
      WORKING_DIRECTORY "${destination}"
      RESULT_VARIABLE extract_result
    )
    if(NOT extract_result EQUAL 0)
      message(FATAL_ERROR "Failed to extract ${filename}")
    endif()
    foreach(required IN LISTS ARGN)
      if(NOT EXISTS "${destination}/${required}")
        message(FATAL_ERROR "${filename} is missing ${required}")
      endif()
    endforeach()
    file(WRITE "${destination}/.duanju-sha256" "${digest}")
  endif()
endfunction()

set(LIBMPV_SRC "${CMAKE_BINARY_DIR}/libmpv")
# 为什么不再直连 GitHub release：上游 shinchiro/mpv-winbuild-cmake 只保留最近若干个
# 每日构建，旧 tag 连同资源会被删掉。原来钉的 20260921 已经 404，CI 上表现为
# 「Failed to download ...: 22;HTTP response code said error」，看起来像网络抖动，
# 实际是资源没了——重试再多次也没用。
#
# 换到同一 mpv 提交（e76a35ec95）在 SourceForge 的永久镜像：SourceForge 不删旧文件，
# 日期比 GitHub 早一天是打包时间差，DLL 由同一份源码构建。内容变了，哈希随之更新。
duanju_native_archive(
  "mpv-dev-x86_64-20260920-git-e76a35ec95.7z"
  "$ENV{DUANJU_MPV_ARCHIVE}"
  "https://downloads.sourceforge.net/project/mpv-player-windows/libmpv/mpv-dev-x86_64-20260920-git-e76a35ec95.7z"
  "60f9102db46aea8cef9bfb4345ee6a106f34fdbd1df9587e38f0660688039341"
  "${LIBMPV_SRC}"
  "libmpv-2.dll" "libmpv.dll.a" "include/mpv/client.h" "include/mpv/render.h"
)
file(COPY "${LIBMPV_SRC}/include/mpv/" DESTINATION "${LIBMPV_SRC}/include")

set(ANGLE_SRC "${CMAKE_BINARY_DIR}/ANGLE")
duanju_native_archive(
  "ANGLE-v1.0.1.7z"
  "$ENV{DUANJU_ANGLE_ARCHIVE}"
  "https://github.com/alexmercerind/flutter-windows-ANGLE-OpenGL-ES/releases/download/v1.0.1/ANGLE.7z"
  "cc5911bb15d596fd5a2b362613ad35b7093b427117269a7359054a65746a5f9a"
  "${ANGLE_SRC}"
  "include/EGL/egl.h" "lib/libEGL.dll.lib" "lib/libGLESv2.dll.lib"
  "d3dcompiler_47.dll" "libc++.dll" "libEGL.dll" "libGLESv2.dll"
  "vk_swiftshader.dll" "vulkan-1.dll" "zlib.dll"
)

set(PLUGIN_NAME "media_kit_libs_windows_video_plugin")
add_library(${PLUGIN_NAME} SHARED
  "include/media_kit_libs_windows_video/media_kit_libs_windows_video_plugin_c_api.h"
  "media_kit_libs_windows_video_plugin_c_api.cpp"
)
apply_standard_settings(${PLUGIN_NAME})
set_target_properties(${PLUGIN_NAME} PROPERTIES CXX_VISIBILITY_PRESET hidden)
target_compile_definitions(${PLUGIN_NAME} PRIVATE FLUTTER_PLUGIN_IMPL)
target_include_directories(${PLUGIN_NAME} INTERFACE "${CMAKE_CURRENT_SOURCE_DIR}/include")
target_link_libraries(${PLUGIN_NAME} PRIVATE flutter flutter_wrapper_plugin)

set(media_kit_libs_windows_video_bundled_libraries
  "${LIBMPV_SRC}/libmpv-2.dll"
  "${ANGLE_SRC}/d3dcompiler_47.dll"
  "${ANGLE_SRC}/libc++.dll"
  "${ANGLE_SRC}/libEGL.dll"
  "${ANGLE_SRC}/libGLESv2.dll"
  "${ANGLE_SRC}/vk_swiftshader.dll"
  "${ANGLE_SRC}/vulkan-1.dll"
  "${ANGLE_SRC}/zlib.dll"
  PARENT_SCOPE
)
