package core

import (
	"encoding/hex"
	"testing"
)

// deterministicV5Random 直接回放 JS 基准测试台产出的随机序列（权威值），
// 让 signV5Playback 的随机输入与 JS 逐字节一致，从而只校验非随机逻辑
// （sm3 / v5IV / v5Hash / v5Xmxor / v5Carrier / v5Medusa / proto 编码）。
// 序列来自 hongguo-1.0.4 模块在相同输入下的 5 次 randomBytes 调用：
//
//	[4]=Helios 前缀, [18]=Medusa 进程 nonce, [4]=msg3, [4]=seed nonce, [4]=包尾随机。
func deterministicV5Random() func(int) []byte {
	const seq = "ed300000" +
		"aa0040800040004000400000408080000000" +
		"7b004080" +
		"42540000" +
		"09000040"
	pool, _ := hex.DecodeString(seq)
	pos := 0
	return func(size int) []byte {
		out := append([]byte(nil), pool[pos:pos+size]...)
		pos += size
		return out
	}
}

// TestV5Xmxor 对照 JS playbackV5Xmxor：输入 [1..16]，密钥 sm3("k")（32 字节，贴近真实）。
func TestV5Xmxor(t *testing.T) {
	data := []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}
	key, _ := hex.DecodeString("415435c817d51abc7431708cfa555ce0bda864c3df8bce484d2c7b0339664045")
	got := v5Xmxor(data, key)
	want, _ := hex.DecodeString("a600422ab818aa4ed06b43bd3995b92c")
	if hex.EncodeToString(got) != hex.EncodeToString(want) {
		t.Fatalf("v5Xmxor 不符：got %s want %s", hex.EncodeToString(got), hex.EncodeToString(want))
	}
}

// TestV5Proto 对照 JS playbackV5Proto 的编码结果。
func TestV5Proto(t *testing.T) {
	got := v5ProtoFields([]v5Field{
		numField(1, 1), numField(2, 2), strField(3, "8662"), strField(4, "7300000000000000001"),
	})
	want, _ := hex.DecodeString("080210041a0438363632221337333030303030303030303030303030303031")
	if hex.EncodeToString(got) != hex.EncodeToString(want) {
		t.Fatalf("v5Proto 不符：got %s want %s", hex.EncodeToString(got), hex.EncodeToString(want))
	}
}

// TestV5Helios 对照 JS playbackV5Helios（ts=1763542800，确定性随机）。
func TestV5Helios(t *testing.T) {
	got := v5Helios(1763542800, deterministicV5Random())
	want := "7TAAAARmuhZ0hlDcbPJP2kTs35XYq9RlMGyFrpgy+TEodPau"
	if got != want {
		t.Fatalf("v5Helios 不符：got %s want %s", got, want)
	}
}

// TestSignV5Playback 端到端对照 JS signPlaybackV5 官方输出（随机序列 [4,18,4,4,4]）。
func TestSignV5Playback(t *testing.T) {
	values := map[string]string{
		"aid": "8662", "app_name": "novelread", "version_code": "74332", "version_name": "7.4.3.32",
		"channel": "vivo_8662_64", "device_platform": "android", "device_type": "PGT-AN10",
		"os_version": "12", "update_version_code": "74332",
		"iid": "7400000000000000001", "cdid": "0123456789abcdef", "device_id": "7300000000000000001",
	}
	body := []byte(`{"mixed_video_id_map":{"1004":["1234567890"]},"biz_param":{"device_level":1}}`)
	gotURL, headers, err := signV5Playback(
		"https://api5-normal-sinfonlinea.fqnovel.com/novel/player/multi_video_model/player/v1",
		values, body, 1763542800123, deterministicV5Random())
	if err != nil {
		t.Fatalf("signV5Playback 出错: %v", err)
	}
	const wantURL = "https://api5-normal-sinfonlinea.fqnovel.com/novel/player/multi_video_model/player/v1?_rticket=1763542800129&aid=8662&app_name=novelread&cdid=0123456789abcdef&channel=vivo_8662_64&device_id=7300000000000000001&device_platform=android&device_type=PGT-AN10&iid=7400000000000000001&os_version=12&update_version_code=74332&version_code=74332&version_name=7.4.3.32"
	if gotURL != wantURL {
		t.Fatalf("URL 不符：\n got %s\nwant %s", gotURL, wantURL)
	}
	if headers["X-Khronos"] != "1763542800" {
		t.Fatalf("X-Khronos 不符：got %s", headers["X-Khronos"])
	}
	const wantHelios = "7TAAAARmuhZ0hlDcbPJP2kTs35XYq9RlMGyFrpgy+TEodPau"
	if headers["X-Helios"] != wantHelios {
		t.Fatalf("X-Helios 不符：got %s", headers["X-Helios"])
	}
	const wantStub = "AF8CEED5730BF22293F2B46BC1AA1C7A"
	if headers["X-SS-STUB"] != wantStub {
		t.Fatalf("X-SS-STUB 不符：got %s", headers["X-SS-STUB"])
	}
	const wantMedusa = "FYcdaT3MUqNZ8hAqLzKzRTLq0T9CVAAB+8opABBEgYsDWNn3jOnE7Nk2j6q8krjdRCKpY6lhQc+6CnlP/O65nrCzTbioZCcuos64DUspZUtOpEqhlL8QnD/L6k8VlMVJKQ2W7WWUHbgPbpQvHJqq7DJUBqzj68Tu+2Vr7rqn59NkzVzb3e/rTgyEFdPiUuEKOKAmFuXPonSA+jy58/cWnBEn9AVl0BdC5ieSGcuzf2wD8PMEkAs5+HLItxJH+EYv22CVkorQ2q77JTWqubhAqcd/SBCMz9Mptugo/dHddLaT6xPsW4hRduzLX3oc0M64zO9OP504Xn0KlN/DQfsE0rqXqAlSFPQJJA3wREm+p2xZYdFws5kQOkrDPSrEbO1stocy3nRhJWAXlkQKOE9Ax3ZczYTNdixQ/dM/LXyjSb+c4BI93y12Y0rR8P2Ybx3SXmaKyFMJFpWuYG9d7bCTc8tguPj10rDYfQjY7mPCUnuJX87iWj8qIM+AYHQEavg5g7Xphkl+rajywP7BV0TNL8bAJHyLbQfYieqgU06UQK5LIyI5s8JZfdLivkHsw2QO0j87ZUgxHbjILh2BNFUIw/VZUaktI06r+CGfrOU2konnjhYUuLzj+8hAlHnUevFJUOPMcEuDUudv//////////8AAA=="
	if headers["X-Medusa"] != wantMedusa {
		t.Fatalf("X-Medusa 不符（长度 got %d want %d）：\n got %s\nwant %s",
			len(headers["X-Medusa"]), len(wantMedusa), headers["X-Medusa"][:40], wantMedusa[:40])
	}
}
