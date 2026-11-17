package core

// 红果 App 播放接口的 V5 签名（X-Helios / X-Medusa）与取流调用。
//
// 取流接口 multi_video_model 不走通用的 X-Gorgon/X-Argus 玩家签名，而是另一套
// 专用 V5 签名。此文件按 hongguo-1.0.4 站源模块的 main.js 逐行移植，随机源可注入，
// 便于测试用 JS 产出的基准值逐字节校验移植正确性。

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// playbackV5SboxHex 是 1024 字节的 S 盒（4 个变体各 256 字节），与 JS 完全一致。
const playbackV5SboxHex = ("53290fe9e51f316f90f74a7e034d367784ab49237cd0135cdc632c322fd3be0e2624a417ecd9151a7ff64f60730a87f9445ec6ada2f865d22b696c4188f24c0d" +
	"f37b75510b939f6bc2fe6883e68bc8c722181ba0bdb08c9871b50c9a16b46eea8eed20ba4b8a14e0fcf1468dbbef05a93e5a9d7dcf06898635c1fd99f5de287a" +
	"80c594daaab19b6279fbd75070cb52eb2555c456e39e7839853874e2f0013ac0b64742d8ca10e4b79c955de8b843ce4ed464a6a72ed508091c92e7b3eedd1e1d" +
	"d12afac321618fa3d65f9167c982f43d66a53c6a1997585b346dbf9672ff37cdbc073b2dae11453304df00815476b2a13fccdb30b957a8acafe1274002485912" +
	"38922563e36406d37a24100b7902a028044e218453c2a399199c9d13dc40c55ba67e502273850ebdc1a2e2188b8f2d9e3cb909b8c46a2a9f4a6dd93007eafa65" +
	"b7b3a881aa908cc74f0ad76bbbb58617c37012ca05a469fd44e62eecf1e4272ce860c0ae5eadf99b20d8f0234174006f3f110d7657d01645d4eff8f71471cbfe" +
	"1cb2f52648727bf3c9de55153e031b4d61757db63dbf7782eeafe9018a8891362fb14759ac32bab03b349aeb5a377c5156be4c8dfbe13a684bda39cf836697c8" +
	"94545fe0f2491acea9fc6252f4ff1e0fe72bd2df6e67a7f635e5bcc6d1a5dd951d89960887a18eb40c785cdb1f80ed42299333ab98cdd67f316cd54346585dcc" +
	"d759fb722789b08807cc0fda293f7e5e1819bb0353517020b31d3d00374952bea2c421e862040a9ec892a05b266b7a0d0cc316f483b81af39715657ffe32ad8d" +
	"e0c13077646035236ee5363e5f900e9a7cf850a81cd9960b6df73317226f99eaa6fff15cec34acb98246639b918ab2851466a7cdfc4ad1b52c2ae95a58d3734e" +
	"47ddd5b7560193f644ef9c408b7dee39b4d6aabf4de761e3252d4c7ba3843187745d55e6a5db05d42b45caed43c0c5d0b64bfa9d94a4a9d8f2f0e1a108dcfd28" +
	"8fd2bc1fc7bd675412138169cb09249875ba3b68cfb11180afae104fc9789571c6dfce02413886796ae21e3c488cc21b9f423a066c2eabf98e2fdeeb5776e4f5" +
	"d09a515ba58cbbab3e37b09fd4f6d9244df80512e84cd7956be301cf64f1429d58577cc241f3a2c655144f81aaf95a4e108da894b38970561ae9002619fbbac9" +
	"1fa922ed202bfc62cb08e0de2a7f3631435f8b47c8a3c563b4db4071ce74c46da6fa696a9e282d1e825d663b93bfd56c48dce1884bb10fccea063929f746443f" +
	"d830a4232e53da21a1f2d1bdfebc3a02c10a1b8ad31d04fd616590d272ac737e98eb33af60528716bee78ec0ee8699031177277d9cff32542c67caf5f4459168" +
	"1335ae0e178f3409a7803db83892dde5b9b7f0c750e24aef077badd678a09b850b84ec1597e4963cb5e675df1c0d6fb618496ecd76c35c798325590c2fb27a5e")

var playbackV5Magic, _ = hex.DecodeString("2d4b4fca49750d433fb5ae2c226dcc56")

var playbackV5SignKey, _ = hex.DecodeString("b23c2a470a20d105dc3f8c4438f8272ed46d6c89ffdcdedd5108698d26e9489d")

var playbackV5HashTable = [64]uint32{
	0xebb64faf, 0x07aadcc2, 0xcf3187bf, 0xe01138ff, 0x6d0bfcff, 0x5a30a3be, 0xb41ad638, 0x34180eb8,
	0xf233eb6f, 0xb1a584cc, 0xccc30dc7, 0x47d1db51, 0xd55653de, 0x70a84fa1, 0x57473c12, 0xf76f0288,
	0x2c077f0a, 0xda0dcad0, 0xfbb86f6c, 0xfdc4cf00, 0x688a020d, 0xe676c6a6, 0x8cd6338b, 0x1a3c8d0e,
	0xcce8b06b, 0x6ad0ed0b, 0xa0522717, 0xdc71ac83, 0x2285db71, 0xd5b4dda6, 0x736f8650, 0x6560306c,
	0x617ce2a6, 0xe423417e, 0x0a40e143, 0x544e4032, 0x88dffb2a, 0x716c1ae0, 0x4c467a88, 0x05b23bb3,
	0xe1d0b866, 0xbaa3dcb8, 0xae3374d3, 0xc3381a50, 0x1702f75b, 0xfe6da368, 0xf0b4cf48, 0x4e0ffbb8,
	0x72aad10d, 0x26c53a3d, 0xf2bce0f6, 0xb4557581, 0x4a257fdd, 0x8c3182a2, 0xab0b3b86, 0x3d5dfb14,
	0x4f103634, 0xd37b52d7, 0x444eff16, 0xeb0a33d1, 0x6ca86f6e, 0x00284ba7, 0x08387cfa, 0x5fb37586,
}

func v5Rotl32(v uint32, bits int) uint32 {
	n := uint(bits & 31)
	if n == 0 {
		return v
	}
	return (v << n) | (v >> (32 - n))
}

func v5Rotl8(v byte, bits int) byte {
	n := uint(bits & 7)
	if n == 0 {
		return v
	}
	return (v << n) | (v >> (8 - n))
}

func v5Rotl64(v uint64, bits int) uint64 {
	n := uint(bits & 63)
	if n == 0 {
		return v
	}
	return (v << n) | (v >> (64 - n))
}

func v5Le32(v uint32) []byte {
	return []byte{byte(v), byte(v >> 8), byte(v >> 16), byte(v >> 24)}
}

func v5U32le(b []byte, off int) uint32 {
	return uint32(b[off]) | uint32(b[off+1])<<8 | uint32(b[off+2])<<16 | uint32(b[off+3])<<24
}

func v5Concat(parts ...[]byte) []byte {
	total := 0
	for _, p := range parts {
		total += len(p)
	}
	out := make([]byte, 0, total)
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

func v5Pad16(b []byte) []byte {
	padding := 16 - len(b)%16
	out := append([]byte(nil), b...)
	for i := 0; i < padding; i++ {
		out = append(out, byte(padding))
	}
	return out
}

// v5IV 计算 V5 哈希的初始化向量。
func v5IV(queryHash, bodyMd5 []byte, ts uint32) uint32 {
	value := uint32(0x20230928)
	for _, data := range [][]byte{queryHash, bodyMd5, v5Le32(ts)} {
		for index, b := range data {
			if index&1 == 0 {
				value = (value >> 4) ^ value ^ (value << 6) ^ uint32(b)
			} else {
				value = ^((value >> 7) ^ value ^ (uint32(b) | (value << 12)))
			}
		}
	}
	return value
}

// v5Hash 是 V5 专用压缩（非标准 SM3），输出 20 字节。
// 要求 IV 的低 4 位为 12，否则 signV5Playback 会换下一个 ticket 重试。
func v5Hash(queryHash, bodyMd5 []byte, ts uint32) ([]byte, error) {
	if len(queryHash) != 32 || len(bodyMd5) != 16 {
		return nil, errors.New("V5 哈希需要 32 字节 query 哈希与 16 字节 body 哈希")
	}
	if v5IV(queryHash, bodyMd5, ts)&15 != 12 {
		return nil, errors.New("V5 哈希分支不受支持")
	}
	const iv = uint32(0x9d4a97c2)
	count := iv + ts
	table := make([]uint32, len(playbackV5HashTable))
	for i, v := range playbackV5HashTable {
		table[i] = v5Rotl32(v, -int((count+1)&255))
	}
	shift := int(count+2) & 7
	padding := make([]byte, 4)
	for i, v := range []byte{0xfa, 0x45, 0x61, 0xd7} {
		// 对应 JS：(value | (value << 8)) 是 16 位 0xXXXX 后再逻辑右移。
		// 不能用 byte（8 位），否则 v<<8 溢出为 0。
		w := uint16(v) | uint16(v)<<8
		padding[i] = byte(w>>uint(shift)) & 0xff
	}
	data := v5Concat(queryHash, bodyMd5, v5Le32(ts), padding, []byte{0, 0, 0, 0, 0, 0, 1, 0xa0})
	words := make([]uint32, 0, 128)
	for index := 0; index < len(data); index += 4 {
		words = append(words, uint32(data[index])<<24|uint32(data[index+1])<<16|uint32(data[index+2])<<8|uint32(data[index+3]))
	}
	for index := 0; index < 112; index++ {
		x, y := words[index+1], words[index+14]
		words = append(words, words[index]+words[index+9]+
			(v5Rotl32(x, 14)^v5Rotl32(x, 25)^(x>>3))+
			(v5Rotl32(y, 13)^v5Rotl32(y, 15)^(y>>10)))
	}
	init := [8]uint32{0x7aba4fc8, 0x67166507, 0x6403fa00, 0x340f512f, 984304912, 3005047866, 2874125293, 2152413264}
	initial := make([]uint32, 8)
	for i, v := range init {
		initial[i] = v5Rotl32(v, -int(count))
	}
	state := make([]uint32, 8)
	copy(state, initial)
	for index := 0; index < 96; index++ {
		base := iv + uint32(index)
		choose := (state[4] ^ state[0]) & state[1]
		choose ^= state[4]
		sigma1 := v5Rotl32(state[1], 26) ^ v5Rotl32(state[1], 21) ^ v5Rotl32(state[1], 7)
		mixed := words[base&127] + choose + sigma1 + table[base&63] + state[6]
		sigma0 := v5Rotl32(state[3], 30) ^ v5Rotl32(state[3], 19) ^ v5Rotl32(state[3], 10)
		majority := state[3]&state[5] | ((state[3] | state[5]) & state[2])
		old := state[7]
		for i := 7; i > 0; i-- { // state.unshift(state.pop())
			state[i] = state[i-1]
		}
		state[0] = old
		state[7] = sigma0 + majority + mixed
		state[0] = old + mixed
	}
	digest := make([]byte, 32)
	for index := 0; index < 8; index++ {
		binary.BigEndian.PutUint32(digest[index*4:], state[index]+initial[index])
	}
	folded := make([]byte, 16)
	for index := 0; index < 16; index++ {
		folded[index] = digest[index] ^ digest[index+16]
	}
	check := uint32(0x20220420)
	for index := 0; index < 12; index++ {
		var temp uint32
		if index&1 == 0 {
			temp = (check >> 3) ^ check
			check = (uint32(folded[index]) ^ (check << 7)) ^ temp
		} else {
			temp = (check >> 5) ^ check
			check = (^(uint32(folded[index]) | (check << 11))) ^ temp
		}
	}
	return v5Concat(folded, v5Le32((((check | 4) ^ 0x1000000) | 0x20))), nil
}

// v5Xmxor 是 V5 的自研流密码。
func v5Xmxor(data, key []byte) []byte {
	encoded := make([]byte, len(data))
	keyAt := func(i int) byte {
		if i < len(key) {
			return key[i]
		}
		return 0 // 与 JS 一致：key 越界读出 undefined，位运算时等价 0
	}
	for index, b := range data {
		at := (index * 4) & 28
		d0, d1 := keyAt(at), keyAt(at+1)
		mixed := byte(int(v5Rotl8(b, 4)) + int(d0))
		mixed = ^mixed ^ d1
		mixed = v5Rotl8(mixed, 3)
		mixed += d1
		mixed ^= d0
		encoded[len(data)-index-1] = ^mixed
	}
	lastIdx := len(encoded) - 1
	last := encoded[lastIdx] ^ encoded[lastIdx-1]
	first := encoded[0]
	encoded[0] = (^last) + first
	encoded[1] = (encoded[0] ^ encoded[lastIdx] ^ 254) + encoded[1]
	encoded[2] += byte(((int(last) - int(first)) ^ int(v5Rotl8(encoded[1], 3)) ^ 2) & 0xff)
	for index := 0; index < len(encoded)-4; index++ {
		mixed := v5Rotl8(encoded[index+2], 3) ^ encoded[index+1] ^ byte(index+3)
		encoded[index+3] = (^mixed) + encoded[index+3]
	}
	encoded[lastIdx] ^= encoded[lastIdx-1]
	var sum uint8
	for index := 1; index < len(encoded); index++ {
		sum += encoded[index]
	}
	encoded[0] = (encoded[0] ^ encoded[1]) + sum
	return encoded
}

// v5Carrier 是 V5 的分组加密（自研 AES 变体 + 比特重排）。
func v5Carrier(key, iv, source []byte, ts uint32) ([]byte, error) {
	if len(key) != 16 || len(iv) != 16 || len(source) < 248 {
		return nil, errors.New("V5 carrier 需要 16 字节密钥/IV 与至少 248 字节源数据")
	}
	sbox, err := hex.DecodeString(playbackV5SboxHex)
	if err != nil {
		return nil, err
	}
	variant := int(ts & 3)
	box := sbox[variant*256 : (variant+1)*256]
	conTables := [4][4]int{{2, 0, 3, 1}, {1, 3, 2, 0}, {3, 2, 1, 0}, {2, 3, 1, 0}}
	orderTables := [4][16]int{
		{0, 9, 14, 15, 4, 13, 2, 7, 8, 1, 6, 3, 12, 5, 10, 11},
		{0, 9, 14, 11, 4, 13, 2, 3, 8, 1, 6, 15, 12, 5, 10, 7},
		{0, 9, 14, 3, 4, 13, 2, 15, 8, 1, 6, 11, 12, 5, 10, 7},
		{0, 9, 14, 15, 4, 13, 2, 3, 8, 1, 6, 11, 12, 5, 10, 7},
	}
	initials := [4]uint32{0xbbb5de72, 0xd4638d5b, 0x92a5f73b, 0x89f8a1c9}
	con := conTables[variant]
	order := orderTables[variant]
	initial := initials[variant]
	initialBytes := v5Le32(initial)
	expanded := make([]byte, 48)
	for index := 0; index < 16; index++ {
		expanded[index] = initialBytes[index&3] ^ key[index]
	}
	rounds := 8
	for index := 4; index < 12; index++ {
		at := 4 * (index - 1)
		a, b, c, d := expanded[at], expanded[at+1], expanded[at+2], expanded[at+3]
		if index&3 == 0 {
			next := byte((initial >> uint(rounds&24)) ^ uint32(box[b]))
			b, c, d, a = box[c], box[d], box[a], next
		}
		rounds += 2
		expanded[at+4] = a ^ expanded[at-12]
		expanded[at+5] = b ^ expanded[at-11]
		expanded[at+6] = c ^ expanded[at-10]
		expanded[at+7] = d ^ expanded[at-9]
	}
	xt := func(v byte) byte {
		r := v << 1
		if v&0x80 != 0 {
			r ^= 0x1b
		}
		return r
	}
	addCon := func(rows *[4][4]byte, offset int) {
		for i := 0; i < 4; i++ {
			for j := 0; j < 4; j++ {
				rows[i][j] ^= expanded[(offset+i)*4+con[j]]
			}
		}
	}
	block := func(value []byte) []byte {
		var rows [4][4]byte
		for i := 0; i < 4; i++ {
			copy(rows[i][:], value[i*4:i*4+4])
		}
		addCon(&rows, 0)
		for round := 1; round <= 2; round++ {
			for i := 0; i < 4; i++ {
				for j := 0; j < 4; j++ {
					rows[i][j] = box[rows[i][j]]
				}
			}
			var perm [4][4]byte
			for i := 0; i < 4; i++ {
				perm[i] = rows[con[i]]
			}
			flat := make([]byte, 16)
			for i := 0; i < 4; i++ {
				copy(flat[i*4:], perm[i][:])
			}
			var reordered [4][4]byte
			for n := 0; n < 16; n++ {
				reordered[n/4][n%4] = flat[order[n]]
			}
			rows = reordered
			if round == 1 {
				var after [4][4]byte
				for i := 0; i < 4; i++ {
					for j := 0; j < 4; j++ {
						after[i][j] = rows[i][con[j]]
					}
				}
				for index := 0; index < 4; index++ {
					mixed := after[0][index] ^ after[1][index] ^ after[2][index] ^ after[3][index]
					firstCol := after[0][index]
					after[0][index] ^= mixed ^ xt(after[0][index]^after[1][index])
					after[1][index] ^= mixed ^ xt(after[1][index]^after[2][index])
					after[2][index] ^= mixed ^ xt(after[2][index]^after[3][index])
					after[3][index] ^= mixed ^ xt(after[3][index]^firstCol)
				}
				rows = after
			}
			addCon(&rows, round*4)
		}
		for i := 0; i < 4; i++ {
			for j := 0; j < 4; j++ {
				rows[i][j] ^= expanded[(4+i)*4+j]
			}
		}
		out := make([]byte, 16)
		for i := 0; i < 4; i++ {
			copy(out[i*4:], rows[i][:])
		}
		return out
	}
	wire := [8]int{6, 5, 3, 4, 2, 7, 1, 0}
	compact := [8]int{1, 7, 3, 6, 2, 0, 4, 5}
	plaintext := make([]byte, 32)
	for i := 0; i < 31; i++ {
		for j := 0; j < 8; j++ {
			plaintext[i] |= (source[i*8+j] >> uint(wire[j])) & 1 << uint(compact[j])
		}
	}
	plaintext[31] = 1
	input := make([]byte, 16)
	for index := 0; index < 16; index++ {
		input[index] = plaintext[index] ^ iv[index]
	}
	firstOut := block(input)
	for index := 0; index < 16; index++ {
		input[index] = plaintext[index+16] ^ firstOut[index]
	}
	key2 := make([]byte, 32)
	copy(key2, firstOut)
	copy(key2[16:], block(input))
	out := make([]byte, 1+len(source))
	out[0] = key2[31]
	copy(out[1:], source)
	for i := 0; i < 31; i++ {
		for j := 0; j < 8; j++ {
			at := 1 + i*8 + j
			out[at] &^= byte(1 << wire[j])
			out[at] |= byte((uint(key2[i])>>uint(compact[j]))&1) << uint(wire[j])
		}
	}
	return out, nil
}

// v5Helios 生成 X-Helios 头（与 hongguoCommentLadon 同构，仅 md5 输入与常量串不同）。
func v5Helios(ts uint32, random func(int) []byte) string {
	prefix := random(4)
	sum := md5.Sum(v5Concat(prefix, []byte("8662")))
	ascii := []byte(hex.EncodeToString(sum[:]))
	left, right := binary.LittleEndian.Uint64(ascii[0:8]), binary.LittleEndian.Uint64(ascii[8:16])
	queue := []uint64{binary.LittleEndian.Uint64(ascii[16:24]), binary.LittleEndian.Uint64(ascii[24:32])}
	keys := make([]uint64, 0, 34)
	for index := 0; index < 34; index++ {
		keys = append(keys, left)
		mixed := (v5Rotl64(right, -8) + left) ^ uint64(index)
		queue = append(queue, mixed)
		left = mixed ^ v5Rotl64(left, 3)
		right, queue = queue[0], queue[1:]
	}
	data := v5Pad16([]byte(strconv.FormatUint(uint64(ts), 10) + "-646637794-8662"))
	for offset := 0; offset < len(data); offset += 16 {
		a, b := binary.LittleEndian.Uint64(data[offset:]), binary.LittleEndian.Uint64(data[offset+8:])
		for _, key := range keys {
			b = key ^ (a + v5Rotl64(b, -8))
			a = b ^ v5Rotl64(a, 3)
		}
		binary.LittleEndian.PutUint64(data[offset:], a)
		binary.LittleEndian.PutUint64(data[offset+8:], b)
	}
	return base64.StdEncoding.EncodeToString(v5Concat(prefix, data))
}

type v5Who struct {
	DeviceID, OSVersion, DeviceType, Channel, VersionName, DeviceBrand string
}

// v5Medusa 生成 X-Medusa 头。随机调用顺序（须与 JS 一致）：
// random(18) 进程 nonce、random(4) message[3]、random(4) 哈希 seed、random(4) 包尾随机。
func v5Medusa(queryHash, f13 []byte, ts uint32, who v5Who, nowMs int64, random func(int) []byte) (string, error) {
	processNonce := base64.RawURLEncoding.EncodeToString(random(18))
	deviceInfo := v5ProtoFields([]v5Field{
		numField(1, 1), numField(2, 2), strField(3, "8662"), strField(4, who.DeviceID),
		strField(5, processNonce), strField(6, "!noperm!"), strField(12, "Asia/Shanghai,8"), strField(13, "zh_CN"),
		strField(22, who.OSVersion), numField(25, int(nowMs)), strField(30, who.DeviceType),
		strField(31, who.DeviceBrand), strField(32, who.DeviceType), strField(34, who.DeviceBrand),
	})
	env := v5ProtoFields([]v5Field{
		strField(6, "v04.10.03.04-bugfix"), strField(8, who.Channel), byteField(12, deviceInfo), strField(14, who.VersionName),
	})
	msg3 := v5U32le(random(4), 0)
	nonce := v5U32le(random(4), 0)
	nested := v5ProtoFields([]v5Field{numField(1, 1), numField(2, 1), numField(3, 1), numField(5, 1)})
	message := v5ProtoFields([]v5Field{
		byteField(1, playbackV5Magic), numField(2, 5), numField(3, int(msg3)), strField(4, "8662"),
		strField(5, who.DeviceID), strField(6, "646637794"), strField(7, who.VersionName),
		strField(8, "v04.10.03-ml-android"), numField(9, 0x040a0300), byteField(10, make([]byte, 8)),
		numField(12, int(ts)), byteField(13, f13), byteField(14, queryHash[:6]), byteField(15, nested),
		numField(17, int(ts)), strField(20, "none"), numField(21, 377), byteField(23, env),
		strField(24, `{"cmr":16777216,"cmr2":16777216,"un_h":11,"vpn":0,"kd":0,"dyn":"","do":1,"tk":true}`),
	})
	// 与 JS 一致：xmxor 密钥是 sm3(signKey + Le32(nonce) + signKey)（32 字节），不是 md5。
	hashArr := hongguoSM3(v5Concat(playbackV5SignKey, v5Le32(nonce), playbackV5SignKey))
	hashSum := hashArr[:]
	high := uint32(nonce>>16) & 0xff
	seedInner := (high << 11) | (nonce >> 24)
	seedInner ^= high >> 5
	seedInner ^= high
	seed := v5Le32(^seedInner)
	transformed := v5Concat(make([]byte, 8), v5Xmxor(message, hashSum))
	for i, j := 0, len(transformed)-1; i < j; i, j = i+1, j-1 { // .reverse()
		transformed[i], transformed[j] = transformed[j], transformed[i]
	}
	for index := range transformed {
		transformed[index] ^= seed[^(index)&3]
	}
	check := uint32(0x18000001) | (uint32(queryHash[0]&63) << 14) | (uint32(f13[0]&63) << 8)
	packedRand := random(4)
	packed := v5Concat([]byte{0xca}, packedRand, v5Le32(check), transformed, []byte{byte(nonce >> 16), byte(nonce >> 24)})
	keyBytes := md5.Sum(playbackV5SignKey[:16])
	ivBytes := md5.Sum(playbackV5SignKey[16:32])
	encrypted, err := v5Carrier(keyBytes[:], ivBytes[:], packed, ts)
	if err != nil {
		return "", err
	}
	prefix := v5Concat(v5Le32(5), playbackV5Magic)
	stamp := v5Le32(ts)
	for index := range prefix {
		prefix[index] ^= stamp[index&3]
	}
	tail := []byte{byte(nonce & 0xff), byte((nonce >> 8) & 0xff), 0, 1}
	return base64.StdEncoding.EncodeToString(v5Concat(prefix, tail, encrypted)), nil
}

// signV5Playback 是顶层入口：循环 ticket 直到 IV 命中分支，产出签名 URL 与请求头。
func signV5Playback(base string, values map[string]string, body []byte, nowMs int64, random func(int) []byte) (string, map[string]string, error) {
	ts := uint32(nowMs / 1000)
	var bodyMd5 []byte
	if body != nil {
		sum := md5.Sum(body)
		bodyMd5 = sum[:]
	} else {
		bodyMd5 = make([]byte, 16)
	}
	for offset := 0; offset < 1024; offset++ {
		params := make(map[string]string, len(values)+1)
		for k, v := range values {
			params[k] = v
		}
		params["_rticket"] = strconv.FormatInt(nowMs+int64(offset), 10)
		query := v5EncodeQuery(params)
		queryHashArr := hongguoSM3([]byte(query))
		queryHash := queryHashArr[:]
		if v5IV(queryHash, bodyMd5, ts)&15 != 12 {
			continue
		}
		who := v5Who{
			DeviceID:    values["device_id"],
			VersionName: values["version_name"],
			OSVersion:   values["os_version"],
			DeviceType:  values["device_type"],
			Channel:     values["channel"],
			DeviceBrand: values["device_brand"],
		}
		for _, required := range []string{who.DeviceID, who.VersionName, who.OSVersion, who.DeviceType, who.Channel} {
			if required == "" {
				return "", nil, errors.New("播放签名缺少设备字段")
			}
		}
		if who.DeviceBrand == "" {
			who.DeviceBrand = "HONOR"
		}
		f13, err := v5Hash(queryHash, bodyMd5, ts)
		if err != nil {
			return "", nil, err
		}
		headers := map[string]string{"X-Khronos": strconv.FormatUint(uint64(ts), 10)}
		headers["X-Helios"] = v5Helios(ts, random)
		medusa, err := v5Medusa(queryHash, f13, ts, who, nowMs, random)
		if err != nil {
			return "", nil, err
		}
		headers["X-Medusa"] = medusa
		if body != nil {
			headers["X-SS-STUB"] = strings.ToUpper(hex.EncodeToString(bodyMd5))
			headers["Content-Type"] = "application/json; charset=utf-8"
		}
		return base + "?" + query, headers, nil
	}
	return "", nil, errors.New("无法在 1024 次 ticket 尝试内生成 V5 播放签名")
}

// v5EncodeQuery 与 JS encodeQuery 对齐：按键排序，空格转成 +。
func v5EncodeQuery(params map[string]string) string {
	v := make(url.Values, len(params))
	for k, val := range params {
		v.Set(k, val)
	}
	return v.Encode()
}

// V5 proto 字段封装。
type v5Field struct {
	num   int
	kind  int // 0 num, 1 str, 2 bytes
	value int
	str   string
	data  []byte
}

func numField(num, value int) v5Field         { return v5Field{num: num, kind: 0, value: value} }
func strField(num int, value string) v5Field  { return v5Field{num: num, kind: 1, str: value} }
func byteField(num int, value []byte) v5Field { return v5Field{num: num, kind: 2, data: value} }

func v5ProtoFields(fields []v5Field) []byte {
	out := make([]byte, 0, 256)
	for _, f := range fields {
		switch f.kind {
		case 0:
			out = binary.AppendUvarint(out, uint64(f.num)*8)
			var zz uint64
			if f.value < 0 {
				zz = uint64(-f.value)*2 - 1
			} else {
				zz = uint64(f.value) * 2
			}
			out = binary.AppendUvarint(out, zz)
		case 1:
			if f.str == "" {
				continue
			}
			out = binary.AppendUvarint(out, uint64(f.num)*8+2)
			out = binary.AppendUvarint(out, uint64(len(f.str)))
			out = append(out, f.str...)
		case 2:
			if len(f.data) == 0 {
				continue
			}
			out = binary.AppendUvarint(out, uint64(f.num)*8+2)
			out = binary.AppendUvarint(out, uint64(len(f.data)))
			out = append(out, f.data...)
		}
	}
	return out
}

// 默认随机源（生产用）：密码学安全随机。
func v5Random(n int) []byte {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return b
}

// ---- 以下为取流调用链（对齐 hongguo-1.0.4）----

const (
	hongguoPlaybackBaseURL   = "https://api5-normal-sinfonlinea.fqnovel.com"
	hongguoPlaybackUserAgent = "com.phoenix.read/73932 (Linux; U; Android 12; zh_CN_#Hans; PGT-AN10; Build/V417IR;tt-ok/3.12.13.20)"
	hongguoPlaybackPath      = "/novel/player/multi_video_model/player/v1"
)

// v5PlaybackQuery 是 1.0.4 取流专用的 query 字段集（与目录接口的 hongguoAppQuery 不同）。
func v5PlaybackQuery(deviceID, installID, cdid string) map[string]string {
	return map[string]string{
		"aid": "8662", "app_name": "novelread", "version_code": "74332", "version_name": "7.4.3.32",
		"channel": "vivo_8662_64", "device_platform": "android", "device_type": "PGT-AN10",
		"os_version": "12", "update_version_code": "74332", "iid": installID, "cdid": cdid,
		"device_id": deviceID,
	}
}

// v5PlaybackPayload 是 1.0.4 取流请求体。
func v5PlaybackPayload(videoID string) []byte {
	body, _ := json.Marshal(map[string]any{
		"mixed_video_id_map": map[string]any{"1004": []string{videoID}},
		"biz_param": map[string]any{
			"detail_page_version": 0, "device_level": 1, "disable_digg_stat": false,
			"disable_video_relate_book": false, "need_all_video_definition": true,
			"need_mp4_align": false, "use_os_player": false, "use_server_dns": false, "video_platform": 1024,
		},
		"dr_scene": "player",
	})
	return body
}

// resolveHongguoV5Media 用 V5 签名调 multi_video_model 取流（1.0.4 取流路径）。
// helper 为 true 时走辅助设备（主设备取不到兼容媒体时轮转用）。
func (d *Downloader) resolveHongguoV5Media(ctx context.Context, videoID string, helper bool) (providerMedia, error) {
	if !hongguoNumericID.MatchString(videoID) {
		return providerMedia{}, errors.New("红果视频 ID 无效")
	}
	client := d.hongguoClient()
	if err := d.ensureHongguoDevice(ctx, helper, ""); err != nil {
		return providerMedia{}, errors.New("红果设备不可用")
	}
	client.mu.Lock()
	deviceID, installID, _ := client.deviceLocked(helper)
	client.mu.Unlock()

	cdid := uuidLike()
	now := time.Now()
	nowMs := now.UnixMilli()
	body := v5PlaybackPayload(videoID)

	signedURL, headers, err := signV5Playback(hongguoPlaybackBaseURL+hongguoPlaybackPath, v5PlaybackQuery(deviceID, installID, cdid), body, nowMs, v5Random)
	if err != nil {
		return providerMedia{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, signedURL, strings.NewReader(string(body)))
	if err != nil {
		return providerMedia{}, err
	}
	request.Header.Set("User-Agent", hongguoPlaybackUserAgent)
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	response, err := d.doPreparedCatalogRequest(request, 20*time.Second, nil)
	if err != nil {
		return providerMedia{}, err
	}
	content, readErr := io.ReadAll(io.LimitReader(response.Body, providerMaxBodyBytes+1))
	response.Body.Close()
	if readErr != nil {
		return providerMedia{}, readErr
	}
	if response.StatusCode != http.StatusOK {
		return providerMedia{}, errors.New("红果 App 取流接口 HTTP " + strconv.Itoa(response.StatusCode))
	}
	if len(content) == 0 {
		return providerMedia{}, errors.New("红果 App 取流接口未返回有效数据")
	}
	var result map[string]any
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.UseNumber()
	if err := decoder.Decode(&result); err != nil || result == nil {
		return providerMedia{}, errors.New("红果 App 取流接口返回格式异常")
	}
	// data[1004][videoID] 或直接 data[videoID]
	data := nestedMap(result, "data")
	inner := data
	if m := nestedMap(data, "1004"); len(m) > 0 {
		inner = m
	}
	row := inner[videoID]
	model, _ := row.(map[string]any)
	if model == nil {
		// video_model 可能是 base64 字符串
		encoded, _ := row.(string)
		if encoded != "" {
			if decoded, err := decodeHongguoBase64(encoded); err == nil && len(decoded) > 0 {
				encoded = string(decoded)
			}
			decoder := json.NewDecoder(strings.NewReader(encoded))
			decoder.UseNumber()
			if err := decoder.Decode(&model); err != nil {
				return providerMedia{}, errors.New("红果 App 播放信息格式异常")
			}
		}
	}
	if len(model) == 0 {
		if rows := anyList(inner["video_list"]); len(rows) > 0 {
			model = map[string]any{"video_list": inner["video_list"], "video_duration": inner["video_duration"], "duration": inner["duration"]}
		}
	}
	return selectHongguoAppMedia(model)
}

// appMediaV5 对齐 JS appMedia：主设备取不到兼容媒体时轮转到辅助设备重试。
func (d *Downloader) appMediaV5(ctx context.Context, videoID string) (providerMedia, error) {
	var lastErr error
	for _, helper := range []bool{false, true} {
		media, err := d.resolveHongguoV5Media(ctx, videoID, helper)
		if err == nil {
			return media, nil
		}
		lastErr = err
	}
	if err := d.ensureHongguoDevice(ctx, true, ""); err != nil {
		return providerMedia{}, errors.New(publicError(lastErr).Error())
	}
	return d.resolveHongguoV5Media(ctx, videoID, true)
}
