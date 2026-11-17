package core

import (
	"encoding/hex"
	"testing"
)

// TestV5MedusaMessageDiff 复刻 v5Medusa 的 message 构造，逐字段对照 JS 捕获值，
// 定位 X-Medusa 分叉到具体哪个 proto 字段。
func TestV5MedusaMessageDiff(t *testing.T) {
	const processNonce = "qgBAgABAAEAAQAAAQICAAAAA"
	deviceInfoWant, _ := hex.DecodeString("080210041a04383636322213373330303030303030303030303030303030312a187167424167414241414541415141414151494341414141413208216e6f7065726d21620f417369612f5368616e676861692c386a057a685f434eb201023132c801f6dbbcb5d366f201085047542d414e3130fa0105484f4e4f528202085047542d414e3130920205484f4e4f52")
	deviceInfo := v5ProtoFields([]v5Field{
		numField(1, 1), numField(2, 2), strField(3, "8662"), strField(4, "7300000000000000001"),
		strField(5, processNonce), strField(6, "!noperm!"), strField(12, "Asia/Shanghai,8"), strField(13, "zh_CN"),
		strField(22, "12"), numField(25, 1763542800123), strField(30, "PGT-AN10"),
		strField(31, "HONOR"), strField(32, "PGT-AN10"), strField(34, "HONOR"),
	})
	if hex.EncodeToString(deviceInfo) != hex.EncodeToString(deviceInfoWant) {
		t.Fatalf("deviceInfo 分叉：\n got %s\nwant %s", hex.EncodeToString(deviceInfo), hex.EncodeToString(deviceInfoWant))
	}

	envWant, _ := hex.DecodeString("32137630342e31302e30332e30342d627567666978420c7669766f5f383636325f3634628e01080210041a04383636322213373330303030303030303030303030303030312a187167424167414241414541415141414151494341414141413208216e6f7065726d21620f417369612f5368616e676861692c386a057a685f434eb201023132c801f6dbbcb5d366f201085047542d414e3130fa0105484f4e4f528202085047542d414e3130920205484f4e4f527208372e342e332e3332")
	env := v5ProtoFields([]v5Field{
		strField(6, "v04.10.03.04-bugfix"), strField(8, "vivo_8662_64"), byteField(12, deviceInfo), strField(14, "7.4.3.32"),
	})
	if hex.EncodeToString(env) != hex.EncodeToString(envWant) {
		t.Fatalf("env 分叉：\n got %s\nwant %s", hex.EncodeToString(env), hex.EncodeToString(envWant))
	}

	nestWant, _ := hex.DecodeString("0802100218022802")
	nested := v5ProtoFields([]v5Field{numField(1, 1), numField(2, 1), numField(3, 1), numField(5, 1)})
	if hex.EncodeToString(nested) != hex.EncodeToString(nestWant) {
		t.Fatalf("nested 分叉：\n got %s\nwant %s", hex.EncodeToString(nested), hex.EncodeToString(nestWant))
	}

	f13, _ := hex.DecodeString("49d5c269c22e146cb973b8859a7ea5ca376b65ec")
	qh6, _ := hex.DecodeString("0ab91a1cbf31")
	msgWant, _ := hex.DecodeString("0a102d4b4fca49750d433fb5ae2c226dcc56100a18f6818084102204383636322a133733303030303030303030303030303030303132093634363633373739343a08372e342e332e333242147630342e31302e30332d6d6c2d616e64726f696448808cd0405208000000000000000060a09cec910d6a1449d5c269c22e146cb973b8859a7ea5ca376b65ec72060ab91a1cbf317a0808021002180228028801a09cec910da201046e6f6e65a801f205ba01be0132137630342e31302e30332e30342d627567666978420c7669766f5f383636325f3634628e01080210041a04383636322213373330303030303030303030303030303030312a187167424167414241414541415141414151494341414141413208216e6f7065726d21620f417369612f5368616e676861692c386a057a685f434eb201023132c801f6dbbcb5d366f201085047542d414e3130fa0105484f4e4f528202085047542d414e3130920205484f4e4f527208372e342e332e3332c201537b22636d72223a31363737373231362c22636d7232223a31363737373231362c22756e5f68223a31312c2276706e223a302c226b64223a302c2264796e223a22222c22646f223a312c22746b223a747275657d")
	message := v5ProtoFields([]v5Field{
		byteField(1, playbackV5Magic), numField(2, 5), numField(3, 2151678075), strField(4, "8662"),
		strField(5, "7300000000000000001"), strField(6, "646637794"), strField(7, "7.4.3.32"),
		strField(8, "v04.10.03-ml-android"), numField(9, 0x040a0300), byteField(10, make([]byte, 8)),
		numField(12, 1763542800), byteField(13, f13), byteField(14, qh6), byteField(15, nested),
		numField(17, 1763542800), strField(20, "none"), numField(21, 377), byteField(23, env),
		strField(24, `{"cmr":16777216,"cmr2":16777216,"un_h":11,"vpn":0,"kd":0,"dyn":"","do":1,"tk":true}`),
	})
	if hex.EncodeToString(message) != hex.EncodeToString(msgWant) {
		gw := hex.EncodeToString(message)
		ww := hex.EncodeToString(msgWant)
		pos := 0
		for pos < len(gw) && pos < len(ww) && gw[pos] == ww[pos] {
			pos++
		}
		t.Fatalf("message 在 hex 偏移 %d 分叉（约字节 %d）：\n got ...%s...\nwant ...%s...",
			pos, pos/2, gw[max(0, pos-24):min(len(gw), pos+48)], ww[max(0, pos-24):min(len(ww), pos+48)])
	}
}
