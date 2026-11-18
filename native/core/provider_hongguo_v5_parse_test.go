package core

import (
	"encoding/json"
	"net/http"
	"testing"
)

// TestParseHongguoV5PlaybackVideoModelString 是回归测试：1.0.4 的取流响应把线路放在
// data["1004"][videoID].video_model 的 JSON 字符串里。早期实现把整个 row 当 model，
// 从不解析该字符串，导致 video_list 为空、红果源放不出来。此测试锁住正确行为。
func TestParseHongguoV5PlaybackVideoModelString(t *testing.T) {
	videoModel := map[string]any{
		"status":         10,
		"video_duration": 127.5,
		"video_list": []any{
			map[string]any{
				"main_url":   "https://media.test/h264-1080.mp4",
				"video_meta": map[string]any{"codec_type": "h264", "vheight": 1080, "definition": "1080p"},
			},
			map[string]any{
				"main_url":   "https://media.test/h264-720.mp4",
				"video_meta": map[string]any{"codec_type": "h264", "vheight": 720, "definition": "720p"},
			},
		},
	}
	encoded, err := json.Marshal(videoModel)
	if err != nil {
		t.Fatal(err)
	}
	videoID := "7708720933577428030"
	response := map[string]any{
		"code": 0,
		"data": map[string]any{
			"1004": map[string]any{
				videoID: map[string]any{
					"disable_vsr": true,
					"video_model": string(encoded), // 关键：JSON 字符串，需二次解析
				},
			},
		},
	}

	media, err := parseHongguoV5Playback(response, videoID)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if media.URL != "https://media.test/h264-1080.mp4" {
		t.Fatalf("wrong URL: %s", media.URL)
	}
	if media.Quality != 1080 {
		t.Fatalf("wrong quality: %d", media.Quality)
	}
	if len(media.Variants) != 2 {
		t.Fatalf("expected 2 variants, got %d", len(media.Variants))
	}
	if media.Referer != "https://novel.snssdk.com/" {
		t.Fatalf("wrong referer: %s", media.Referer)
	}
}

// TestParseHongguoV5PlaybackPlainModel 覆盖 video_model 直接是对象、以及线路直接在 row 的旧格式。
func TestParseHongguoV5PlaybackPlainModel(t *testing.T) {
	videoID := "1234567890"
	// video_model 直接是对象
	obj := map[string]any{
		"code": 0,
		"data": map[string]any{
			videoID: map[string]any{
				"video_model": map[string]any{
					"video_list": []any{map[string]any{
						"main_url":   "https://media.test/plain.mp4",
						"video_meta": map[string]any{"codec_type": "h264", "definition": "480p"},
					}},
				},
			},
		},
	}
	media, err := parseHongguoV5Playback(obj, videoID)
	if err != nil || media.URL != "https://media.test/plain.mp4" {
		t.Fatalf("object model: url=%s err=%v", media.URL, err)
	}

	// 老格式：video_list 直接在 row，无 video_model
	legacy := map[string]any{
		"code": 0,
		"data": map[string]any{
			videoID: map[string]any{
				"video_list": []any{map[string]any{
					"main_url":   "https://media.test/legacy.mp4",
					"video_meta": map[string]any{"codec_type": "h264", "definition": "360p"},
				}},
			},
		},
	}
	media, err = parseHongguoV5Playback(legacy, videoID)
	if err != nil || media.URL != "https://media.test/legacy.mp4" {
		t.Fatalf("legacy model: url=%s err=%v", media.URL, err)
	}
}

// TestHongguoHelperDeviceRegisteredOnce 确保辅助设备会被真正注册（HelperRegistered 标记），
// 而不是拿着初始化时的随机假 ID 直接去请求（会返回空 body）。
func TestHongguoHelperDeviceRegisteredOnce(t *testing.T) {
	d := &Downloader{cfg: defaultConfig(), client: &http.Client{}, limiter: newRequestLimiter(1, 0)}
	// 未注册前，deviceLocked(helper) 应报告未注册（即便已有随机 ID）。
	client := d.hongguoClient()
	client.mu.Lock()
	id, _, registered := client.deviceLocked(true)
	client.mu.Unlock()
	if registered {
		t.Fatalf("helper device reported registered before real registration: id=%s", id)
	}
}
