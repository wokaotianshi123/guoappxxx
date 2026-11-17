package core

import (
	"crypto/md5"
	"encoding/hex"
	"testing"
)

// TestV5IVProbe 用 JS 基准里 offset=50 的固定输入复算 sm3 与 IV，
// 与 JS 逐位对照，定位偏差层（sm3 / v5IV）。
func TestV5IVProbe(t *testing.T) {
	const ts = uint32(1763542800)
	const query = "_rticket=1763542800173&aid=8662&app_name=novelread&cdid=0123456789abcdef&channel=vivo_8662_64&device_id=7300000000000000001&device_platform=android&device_type=PGT-AN10&iid=7400000000000000001&os_version=12&update_version_code=74332&version_code=74332&version_name=7.4.3.32"
	const bodyMd5Hex = "af8ceed5730bf22293f2b46bc1aa1c7a"
	const wantQueryHash = "25d175c9707f8c185de95345d74ca5c25173ff7e7b758b57690e2c57f57c5aae"
	const wantIVFull = "3cdccf8c"

	// 1. sm3(query)
	gotQHash := hongguoSM3([]byte(query))
	gotQHashHex := hex.EncodeToString(gotQHash[:])
	if gotQHashHex != wantQueryHash {
		t.Fatalf("sm3(query) 不符：\n got %s\nwant %s", gotQHashHex, wantQueryHash)
	}
	t.Logf("sm3(query) 一致: %s", gotQHashHex)

	// 2. md5(body)
	bodySum := md5.Sum([]byte(`{"mixed_video_id_map":{"1004":["1234567890"]},"biz_param":{"device_level":1}}`))
	if hex.EncodeToString(bodySum[:]) != bodyMd5Hex {
		t.Fatalf("md5(body) 不符：got %s", hex.EncodeToString(bodySum[:]))
	}

	// 3. v5IV(queryHash, bodyMd5, ts)
	iv := v5IV(gotQHash[:], bodySum[:], ts)
	gotIVHex := hex.EncodeToString([]byte{byte(iv >> 24), byte(iv >> 16), byte(iv >> 8), byte(iv)})
	if gotIVHex != wantIVFull {
		t.Fatalf("v5IV 不符：\n got %s\nwant %s", gotIVHex, wantIVFull)
	}
	t.Logf("v5IV 一致: %s (low nibble=%d)", gotIVHex, iv&15)
}

// TestV5QueryEncoding 校验 Go 的 v5EncodeQuery 与 JS encodeQuery 逐字节一致。
// 这是 sign 命中 offset 一致性的前提：若 query 串不同，sm3/IV 都不同。
func TestV5QueryEncoding(t *testing.T) {
	values := map[string]string{
		"aid": "8662", "app_name": "novelread", "version_code": "74332", "version_name": "7.4.3.32",
		"channel": "vivo_8662_64", "device_platform": "android", "device_type": "PGT-AN10",
		"os_version": "12", "update_version_code": "74332",
		"iid": "7400000000000000001", "cdid": "0123456789abcdef", "device_id": "7300000000000000001",
	}
	values["_rticket"] = "1763542800173"
	got := v5EncodeQuery(values)
	const want = "_rticket=1763542800173&aid=8662&app_name=novelread&cdid=0123456789abcdef&channel=vivo_8662_64&device_id=7300000000000000001&device_platform=android&device_type=PGT-AN10&iid=7400000000000000001&os_version=12&update_version_code=74332&version_code=74332&version_name=7.4.3.32"
	if got != want {
		t.Fatalf("query 编码不符：\n got %s\nwant %s", got, want)
	}
}

// TestV5Hash 直接对照 JS playbackV5Hash 纯函数（offset=6 命中的 queryHash→f13）。
func TestV5Hash(t *testing.T) {
	queryHash, _ := hex.DecodeString("0ab91a1cbf31e11f9f13fd7e176af00b642e8566960274a98067d016fba3a6ae")
	bodyMd5, _ := hex.DecodeString("af8ceed5730bf22293f2b46bc1aa1c7a")
	got, err := v5Hash(queryHash, bodyMd5, 1763542800)
	if err != nil {
		t.Fatalf("v5Hash 出错: %v", err)
	}
	const want = "49d5c269c22e146cb973b8859a7ea5ca376b65ec"
	if hex.EncodeToString(got) != want {
		t.Fatalf("v5Hash 不符：\n got %s\nwant %s", hex.EncodeToString(got), want)
	}
}

// TestV5Carrier 直接对照 JS playbackV5Carrier 纯函数（固定 key/iv/source/ts）。
func TestV5Carrier(t *testing.T) {
	key, _ := hex.DecodeString("d4ceeaacf94e63fe4d16cad4aad8aa74")
	iv, _ := hex.DecodeString("8c40f9208b4ca8c8d75642919f62030a")
	source := make([]byte, 300)
	for i := range source {
		source[i] = byte(i)
	}
	got, err := v5Carrier(key, iv, source, 1763542800)
	if err != nil {
		t.Fatalf("v5Carrier 出错: %v", err)
	}
	want, _ := hex.DecodeString("b200210203008504070829020b0c0d0e0e50311a131015141618391a1b1c1d1c1f20012a33242524272829223b28ad2e2f30313a333035343678393a2b3c3d3e3e004142434445444608494a5b4ccd4e4f10515a53505554561879524b58dd5c5f6061627360e5666728496a7b68ed6c6f70717a6370f5767738797a7b7cfd7c7ec081829380858687c8898a9b8c0d8e8fd09192839015949798b9928b9c9d9c9fa0a1a2b3a425a4a7a889aaabac2daeafb0b1b2a3b4b5b6b7f899b2abbc3dbebfc0c1cac3c0c5c6c6c8e9c2dbcccdcccf90f1d2d3d055d4d698f9d2cbd85ddedfa0e1e2f3e4e5e6e6e8e9eafbe86deceff0d1f2f3f475f4f6f8f9fafbfcfdfeff000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f202122232425262728292a2b")
	if len(got) != 301 {
		t.Fatalf("v5Carrier 输出长度 %d，期望 301", len(got))
	}
	if hex.EncodeToString(got) != hex.EncodeToString(want) {
		// 打印首个分叉点，便于定位
		for i := 0; i < len(got); i++ {
			if got[i] != want[i] {
				t.Fatalf("v5Carrier 在字节 %d 分叉：got %s want %s（前后各 8 字节）",
					i,
					hex.EncodeToString(got[max(0, i-8):min(len(got), i+9)]),
					hex.EncodeToString(want[max(0, i-8):min(len(want), i+9)]))
			}
		}
	}
}
