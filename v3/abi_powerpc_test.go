// Copyright 2026 The CC Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package cc // import "modernc.org/cc/v3"

import (
	"encoding/binary"
	"testing"
)

// These expectations are measurements, not host C layouts. See
// runs/plan-libc-purego-20261029/evidence/g4-probe2.out and
// runs/bool-enum-probe-20261029/receipt.txt in the port workspace.
func TestDarwinPowerPCABI(t *testing.T) {
	for _, arch := range []string{"ppc", "ppc64"} {
		t.Run(arch, func(t *testing.T) {
			abi, err := NewABI("darwin", arch)
			if err != nil {
				t.Fatal(err)
			}
			if abi.ByteOrder != binary.BigEndian || !abi.SignedChar {
				t.Fatalf("byte order/signed char: %+v", abi)
			}
			ptr, boolSize, fieldAlign := 8, 1, 8
			if arch == "ppc" {
				ptr, boolSize, fieldAlign = 4, 4, 4
			}
			for _, tc := range []struct {
				kind                    Kind
				size, align, fieldAlign int
			}{
				{Bool, boolSize, boolSize, boolSize},
				{Enum, 4, 4, 4},
				{Ptr, ptr, ptr, ptr},
				{Long, ptr, ptr, ptr},
				{ULong, ptr, ptr, ptr},
				{Double, 8, 8, fieldAlign},
				{LongLong, 8, 8, fieldAlign},
				{ULongLong, 8, 8, fieldAlign},
				{LongDouble, 16, 16, 16},
			} {
				got := abi.Types[tc.kind]
				if int(got.Size) != tc.size || got.Align != tc.align || got.FieldAlign != tc.fieldAlign {
					t.Errorf("%s: got %+v, want %d/%d/%d", tc.kind, got, tc.size, tc.align, tc.fieldAlign)
				}
			}
			if got := LongDouble64Flag("darwin", arch); got != "-mlong-double-64" {
				t.Errorf("LongDouble64Flag: %q", got)
			}
		})
	}
}

func TestDarwinPPCFirstMemberLayout(t *testing.T) {
	const src = `
typedef long long ll; typedef long double ld;
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
`
	for _, gcc := range []bool{false, true} {
		cfg := &Config{}
		var err error
		cfg.ABI, err = NewABI("darwin", "ppc")
		if err != nil {
			t.Fatal(err)
		}
		cfg.GCCStructs = gcc
		ast, err := Translate(cfg, nil, nil, []Source{{Name: "darwin-ppc-layout.c", Value: src}})
		if err != nil {
			t.Fatal(err)
		}
		get := func(name string) Type {
			for n := ast.TranslationUnit; n != nil; n = n.TranslationUnit {
				d := n.ExternalDeclaration.Declaration
				if d == nil {
					continue
				}
				for l := d.InitDeclaratorList; l != nil; l = l.InitDeclaratorList {
					if dd := l.InitDeclarator.Declarator; dd.Name().String() == name {
						return dd.Type()
					}
				}
			}
			t.Fatalf("missing %s", name)
			return nil
		}
		for _, tc := range []struct {
			name                            string
			size, align, fieldAlign, offset int
		}{
			{"s_double", 12, 4, 4, 4},
			{"s_ll", 12, 4, 4, 4},
			{"s_ld", 32, 16, 16, 16},
			{"f_double", 16, 8, 4, 8},
			{"f_ll", 16, 8, 4, 8},
			{"lead", 16, 8, 4, 8},
			{"lead8", 16, 8, 4, 8},
			{"f_ld", 32, 16, 16, 16},
			{"inner", 12, 4, 4, 4},
			{"wrap", 20, 4, 4, 4},
		} {
			typ := get(tc.name)
			if int(typ.Size()) != tc.size || typ.Align() != tc.align || typ.FieldAlign() != tc.fieldAlign {
				t.Errorf("%s: size/align/fieldAlign = %d/%d/%d, want %d/%d/%d", tc.name, typ.Size(), typ.Align(), typ.FieldAlign(), tc.size, tc.align, tc.fieldAlign)
			}
			if got := typ.FieldByIndex([]int{1}).Offset(); int(got) != tc.offset {
				t.Errorf("%s: offset = %d, want %d", tc.name, got, tc.offset)
			}
		}
	}
}

// Measured in the Adelie 1.0-beta6 ppc rootfs by Adelie gcc 13.3.0; see
// runs/libc-linuxppc-20261029/capture/{cabi,structs}.out in the port workspace.
// The struct set is TestDarwinPPCFirstMemberLayout's: on linux/ppc (SysV) none
// of darwin's first-member rules apply and every 8-byte scalar is 8-aligned.
func TestLinuxPPCABI(t *testing.T) {
	abi, err := NewABI("linux", "ppc")
	if err != nil {
		t.Fatal(err)
	}
	if abi.ByteOrder != binary.BigEndian || abi.SignedChar {
		t.Fatalf("byte order/signed char: %+v", abi)
	}
	for _, tc := range []struct {
		kind                    Kind
		size, align, fieldAlign int
	}{
		{Bool, 1, 1, 1},
		{Ptr, 4, 4, 4},
		{Long, 4, 4, 4},
		{Double, 8, 8, 8},
		{LongLong, 8, 8, 8},
		{LongDouble, 8, 8, 8},
	} {
		got := abi.Types[tc.kind]
		if int(got.Size) != tc.size || got.Align != tc.align || got.FieldAlign != tc.fieldAlign {
			t.Errorf("%s: got %+v, want %d/%d/%d", tc.kind, got, tc.size, tc.align, tc.fieldAlign)
		}
	}
	if got := LongDouble64Flag("linux", "ppc"); got != "" {
		t.Errorf("LongDouble64Flag: %q, want none (long double is already 64-bit)", got)
	}
	const src = `
typedef long long ll; typedef long double ld;
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
`
	ast, err := Translate(&Config{ABI: abi}, nil, nil, []Source{{Name: "linux-ppc-layout.c", Value: src}})
	if err != nil {
		t.Fatal(err)
	}
	get := func(name string) Type {
		for n := ast.TranslationUnit; n != nil; n = n.TranslationUnit {
			d := n.ExternalDeclaration.Declaration
			if d == nil {
				continue
			}
			for l := d.InitDeclaratorList; l != nil; l = l.InitDeclaratorList {
				if dd := l.InitDeclarator.Declarator; dd.Name().String() == name {
					return dd.Type()
				}
			}
		}
		t.Fatalf("missing %s", name)
		return nil
	}
	for _, tc := range []struct {
		name                            string
		size, align, fieldAlign, offset int
	}{
		{"s_double", 16, 8, 8, 8},
		{"s_ll", 16, 8, 8, 8},
		{"s_ld", 16, 8, 8, 8},
		{"f_double", 16, 8, 8, 8},
		{"f_ll", 16, 8, 8, 8},
		{"lead", 16, 8, 8, 8},
		{"lead8", 16, 8, 8, 8},
		{"f_ld", 16, 8, 8, 8},
		{"inner", 16, 8, 8, 8},
		{"wrap", 24, 8, 8, 8},
	} {
		typ := get(tc.name)
		if int(typ.Size()) != tc.size || typ.Align() != tc.align || typ.FieldAlign() != tc.fieldAlign {
			t.Errorf("%s: size/align/fieldAlign = %d/%d/%d, want %d/%d/%d", tc.name, typ.Size(), typ.Align(), typ.FieldAlign(), tc.size, tc.align, tc.fieldAlign)
		}
		if got := typ.FieldByIndex([]int{1}).Offset(); int(got) != tc.offset {
			t.Errorf("%s: offset = %d, want %d", tc.name, got, tc.offset)
		}
	}
}
