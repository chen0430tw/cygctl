//go:build ignore

package main

import (
	"fmt"
	"unicode/utf8"
)

// detectEncoding reports whether p looks like GBK bytes disguised as UTF-8.
// It uses ASCII characters (spaces, newlines, etc.) as phase-anchor points,
// then measures whether the multibyte sequences that follow each anchor are
// 2-byte (suspicious: GBK blind zone 0xC2–0xDF + 0xA1–0xBF) or
// 3-byte CJK (confirmed UTF-8: 0xE4–0xEF).
//
// GBK characters in the blind zone can only form 2-byte pseudo-UTF-8 pairs,
// while real CJK Unicode characters always encode to 3-byte UTF-8 sequences.
// ASCII anchors reset the phase so measurement starts from a known boundary.
func detectEncoding(p []byte) (likelyGBK bool) {
	suspicious2 := 0
	confirmed3 := 0
	afterAnchor := true // treat stream start as an anchor

	for i := 0; i < len(p); {
		b := p[i]

		if b < 0x80 {
			afterAnchor = true
			i++
			continue
		}

		if afterAnchor {
			afterAnchor = false
			// 3-byte UTF-8 CJK (U+0800–U+FFFF subset: 0xE4–0xEF covers U+4E00–U+9FFF)
			if b >= 0xE4 && b <= 0xEF && i+2 < len(p) &&
				p[i+1]&0xC0 == 0x80 && p[i+2]&0xC0 == 0x80 {
				confirmed3++
				i += 3
				continue
			}
			// 2-byte blind-zone sequence (GBK lead 0xC2–0xDF, trail 0xA1–0xBF)
			if b >= 0xC2 && b <= 0xDF && i+1 < len(p) &&
				p[i+1] >= 0xA1 && p[i+1] <= 0xBF {
				suspicious2++
				i += 2
				continue
			}
		}
		i++
	}

	return suspicious2 > 0 && confirmed3 == 0
}

type testCase struct {
	name      string
	input     []byte
	wantGBK   bool
	wantUTF8  bool // just for documentation; inverse of wantGBK when utf8.Valid
}

func run(tc testCase) (pass bool) {
	isValidUTF8 := utf8.Valid(tc.input)
	gotGBK := detectEncoding(tc.input)

	status := "PASS"
	if gotGBK != tc.wantGBK {
		status = "FAIL"
	}
	fmt.Printf("[%s] %s\n  utf8.Valid=%v  detectGBK=%v  wantGBK=%v\n",
		status, tc.name, isValidUTF8, gotGBK, tc.wantGBK)
	return gotGBK == tc.wantGBK
}

func main() {
	// 通 in GBK = 0xCD 0xA8  (blind zone: lead 0xCD, trail 0xA8)
	// 权 in GBK = 0xC8 0xA8  (blind zone: lead 0xC8, trail 0xA8)
	// 中 in UTF-8 = 0xE4 0xB8 0xAD  (3-byte CJK)
	// 通 in UTF-8 = 0xE9 0x80 0x9A  (3-byte CJK)
	// é  in UTF-8 = 0xC3 0xA9  (2-byte Latin, NOT in trail 0xA1–0xBF? 0xA9 is in range, hmm)
	// → é = 0xC3 0xA9: trail 0xA9 IS in 0xA1–0xBF → suspicious!
	// © in UTF-8 = 0xC2 0xA9: trail 0xA9 in 0xA1–0xBF → also suspicious
	// but these don't appear in Chinese Windows cmd output, acceptable false positive range

	cases := []testCase{
		{
			// Pure GBK blind-zone: "通权" (no ASCII anchors)
			name:    "GBK blind-zone only, no anchors",
			input:   []byte{0xCD, 0xA8, 0xC8, 0xA8},
			wantGBK: true,
		},
		{
			// GBK blind-zone after space anchor: " 通权"
			name:    "GBK blind-zone after space anchor",
			input:   []byte{0x20, 0xCD, 0xA8, 0xC8, 0xA8},
			wantGBK: true,
		},
		{
			// GBK line: "访问 被拒绝\r\n" (spaces reset anchor)
			// 访=0xB7C3 (exclusive, not blind), so use all-blind-zone words
			// "通 权\r\n" in GBK
			name: "GBK line with spaces and CRLF",
			input: []byte{
				0xCD, 0xA8, 0x20, // 通[space]
				0xC8, 0xA8,       // 权
				0x0D, 0x0A,       // \r\n
			},
			wantGBK: true,
		},
		{
			// Multiple GBK lines
			name: "Multiple GBK lines",
			input: []byte{
				0xCD, 0xA8, 0xC8, 0xA8, 0x0D, 0x0A, // 通权\r\n
				0xCA, 0xB3, 0xCB, 0xB0, 0x0D, 0x0A, // (two more blind-zone pairs)\r\n
			},
			wantGBK: true,
		},
		{
			// Real UTF-8 Chinese: "中通" = 0xE4B8AD + 0xE9809A
			name:    "UTF-8 CJK only",
			input:   []byte{0xE4, 0xB8, 0xAD, 0xE9, 0x80, 0x9A},
			wantGBK: false,
		},
		{
			// Real UTF-8 Chinese after anchor: " 中通"
			name:    "UTF-8 CJK after space anchor",
			input:   []byte{0x20, 0xE4, 0xB8, 0xAD, 0xE9, 0x80, 0x9A},
			wantGBK: false,
		},
		{
			// Mixed UTF-8: CJK + blind-zone Latin (é=0xC3A9)
			// confirmed3 > 0 should suppress GBK flag
			name: "UTF-8 mixed CJK + Latin-extended",
			input: []byte{
				0xE4, 0xB8, 0xAD, // 中 (3-byte CJK)
				0x20,             // space
				0xC3, 0xA9,       // é (2-byte, trail in blind zone)
			},
			wantGBK: false,
		},
		{
			// Pure ASCII: no signal either way
			name:    "ASCII only",
			input:   []byte("hello world\r\n"),
			wantGBK: false,
		},
		{
			// GBK exclusive bytes (0x81-0x9F): already caught by containsGBKExclusiveBytes
			// detectEncoding should also handle it via suspicious2
			name:    "GBK exclusive bytes (0x81 lead)",
			input:   []byte{0x20, 0x81, 0x40},
			wantGBK: false, // 0x81 < 0xC2, not in our 2-byte check, but containsGBKExclusiveBytes handles this
		},
		{
			// Typical icacls output pattern: "(F)" in GBK = ASCII, fine
			// "Everyone:(F)" → pure ASCII, no GBK
			name:    "ASCII command output",
			input:   []byte("Everyone:(F)\r\n"),
			wantGBK: false,
		},
		{
			// GBK "拒绝访问" — 拒=0xBE;CE (exclusive, not blind),
			// so this should be caught by containsGBKExclusiveBytes not us.
			// For our function specifically: simulate a buffer where ALL chars are in blind zone
			// "命令" might not be blind zone. Let's use known blind-zone chars.
			// Simulated realistic output: "通知: 权限" in GBK
			// 通=0xCDA8, 知=0xD6AA (0xD6 in range, 0xAA in 0xA1-0xBF ✓ blind)
			// 权=0xC8A8, 限=... let's check: 限 GBK = 0xCFDE (0xDE > 0xBF, NOT blind)
			// Use: 通知 权能 (能=0xC4DC: 0xDC > 0xBF, not blind either)
			// Stick with verified blind-zone pairs
			name: "Realistic GBK command output with colons and spaces",
			input: []byte{
				// "通: 权\r\n" in GBK
				0xCD, 0xA8,       // 通
				0x3A, 0x20,       // ": "  (ASCII anchors)
				0xC8, 0xA8,       // 权
				0x0D, 0x0A,       // \r\n
			},
			wantGBK: true,
		},
		{
			// Edge: single suspicious byte pair only, no anchors
			name:    "Single suspicious pair",
			input:   []byte{0xC5, 0xB2},
			wantGBK: true,
		},
		{
			// UTF-8 output with only 2-byte Latin (no CJK) — known limitation / false positive.
			// é=0xC3A9 (trail 0xA9), ñ=0xC3B1 (trail 0xB1): both in 0xA1–0xBF → suspicious.
			// No 3-byte CJK to suppress the flag → false positive.
			name:    "UTF-8 Latin-extended only (known false positive)",
			input:   []byte{0x20, 0xC3, 0xA9, 0x20, 0xC3, 0xB1}, // " é ñ"
			wantGBK: true, // false positive: documented limitation
		},
		{
			// Known limitation: é (0xC3 0xA9), ñ (0xC3 0xB1), ü (0xC3 0xBC) all have
			// trail bytes in 0xA1–0xBF and no 3-byte CJK to save them.
			// On a GBK system outputting pure Latin-extended UTF-8, this is a false positive.
			// Acceptable in practice: Windows native commands on CP-936 systems don't
			// typically emit standalone Latin-extended characters without CJK context.
			name:    "UTF-8 é alone after anchor (documented false positive)",
			input:   []byte{0x20, 0xC3, 0xA9},
			wantGBK: true, // false positive, but documented and acceptable
		},
	}

	passed, failed := 0, 0
	for _, tc := range cases {
		if run(tc) {
			passed++
		} else {
			failed++
		}
	}
	fmt.Printf("\n%d passed, %d failed\n", passed, failed)
}
