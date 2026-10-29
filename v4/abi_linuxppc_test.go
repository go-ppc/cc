// Copyright 2026 The CC Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package cc // import "modernc.org/cc/v4"

import (
	"encoding/binary"
	"testing"
)

// These expectations are measurements on Adelie 1.0-beta6 ppc (gcc 13.3.0,
// musl), not host C layouts: runs/libc-linuxppc-20261029/capture/{cabi,structs,
// bitfields}.out in the port workspace.
func TestLinuxPPCABI(t *testing.T) {
	abi, err := NewABI("linux", "ppc")
	if err != nil {
		t.Fatal(err)
	}
	if abi.ByteOrder != binary.BigEndian || abi.SignedChar {
		t.Fatalf("byte order/signed char: %v/%v", abi.ByteOrder, abi.SignedChar)
	}
	for _, tc := range []struct {
		kind                    Kind
		size, align, fieldAlign int
	}{
		{Bool, 1, 1, 1},
		{Enum, 4, 4, 4},
		{Ptr, 4, 4, 4},
		{Long, 4, 4, 4},
		{ULong, 4, 4, 4},
		{Double, 8, 8, 8},
		{LongLong, 8, 8, 8},
		{ULongLong, 8, 8, 8},
		{LongDouble, 8, 8, 8},
	} {
		got := abi.Types[tc.kind]
		if int(got.Size) != tc.size || got.Align != tc.align || got.FieldAlign != tc.fieldAlign {
			t.Errorf("%s: got %+v, want %d/%d/%d", tc.kind, got, tc.size, tc.align, tc.fieldAlign)
		}
	}
	// The target is LD64 natively; there is no flag to pass.
	if got := LongDouble64Flag("linux", "ppc"); got != "" {
		t.Errorf("LongDouble64Flag: %q", got)
	}
}

// The darwin/ppc first-member struct set (structs.out) and musl's big-endian
// time64 timespec, whose unnamed 32-bit bitfield puts tv_nsec at 12 in a
// 16-byte struct (bitfields.out). Every struct that embeds a timespec (stat,
// itimerspec, ...) inherits that layout.
func TestLinuxPPCStructLayout(t *testing.T) {
	const src = `
typedef unsigned size_t; typedef long long ll; typedef long double ld;
typedef struct { char c; double x; } s_double;
typedef struct { double x; char c; } f_double;
typedef struct { char c; ll x; } s_ll;
typedef struct { ll x; char c; } f_ll;
typedef struct { int i; double d; } inner;
typedef struct { double d; int i; } lead;
typedef struct { ll x; char c; } lead8;
typedef struct { char c; lead x; } wrap;
typedef struct { char c; ld x; } s_ld;
typedef struct { ld x; char c; } f_ld;
typedef struct { long long tv_sec; int :32; long tv_nsec; int :0; } ts_be;
typedef struct { ts_be a, b; int tail; } stamp;
`
	abi, err := NewABI("linux", "ppc")
	if err != nil {
		t.Fatal(err)
	}
	ast, err := Translate(&Config{ABI: abi}, []Source{{Name: "linux-ppc-layout.c", Value: src + "int __predefined_declarator;"}})
	if err != nil {
		t.Fatal(err)
	}
	get := func(name string) *StructType {
		for _, n := range ast.Scope.Nodes[name] {
			if d, ok := n.(*Declarator); ok && d.IsTypename() {
				return d.Type().(*StructType)
			}
		}
		t.Fatalf("missing %s", name)
		return nil
	}
	for _, tc := range []struct {
		name                            string
		field                           string
		size, align, fieldAlign, offset int
	}{
		{"s_double", "x", 16, 8, 8, 8},
		{"s_ll", "x", 16, 8, 8, 8},
		{"s_ld", "x", 16, 8, 8, 8},
		{"f_double", "c", 16, 8, 8, 8},
		{"f_ll", "c", 16, 8, 8, 8},
		{"lead", "i", 16, 8, 8, 8},
		{"lead8", "c", 16, 8, 8, 8},
		{"f_ld", "c", 16, 8, 8, 8},
		{"inner", "d", 16, 8, 8, 8},
		{"wrap", "x", 24, 8, 8, 8},
		{"ts_be", "tv_nsec", 16, 8, 8, 12},
		{"stamp", "tail", 40, 8, 8, 32},
	} {
		typ := get(tc.name)
		if int(typ.Size()) != tc.size || typ.Align() != tc.align || typ.FieldAlign() != tc.fieldAlign {
			t.Errorf("%s: size/align/fieldAlign = %d/%d/%d, want %d/%d/%d", tc.name, typ.Size(), typ.Align(), typ.FieldAlign(), tc.size, tc.align, tc.fieldAlign)
		}
		f := typ.FieldByName(tc.field)
		if f == nil {
			t.Errorf("%s: no field %s", tc.name, tc.field)
			continue
		}
		if got := f.Offset(); int(got) != tc.offset {
			t.Errorf("%s.%s: offset = %d, want %d", tc.name, tc.field, got, tc.offset)
		}
	}
}
