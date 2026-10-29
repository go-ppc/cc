// Copyright 2026 The CC Authors. All rights reserved
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package cc

import (
	"strings"
	"testing"
)

// __has_extension(c_atomic) must answer from the compilation's own macro
// surface, not from a hardcoded capability list: a TU whose CC defines
// __ATOMIC_RELAXED has C11 atomics, one that does not (Leopard gcc 4.0.1)
// must take the #else branch. Both directions are asserted on purpose: a
// test covering only the true case would pass on a broken evaluator, and the
// probe family in runs/a1-capture-20261029 shows which branch the captured
// toolchain invokes. Written while generating the sqlite engine; see
// parseHasFeature in cpp.go.
func TestHasFeatureCAtomicFromMacroSurface(t *testing.T) {
	src := `#if __has_extension(c_atomic)
int has_c_atomic;
#else
int lacks_c_atomic;
#endif
`

	newPP := func(predef string) (string, error) {
		cfg := defaultCfg()
		cfg.fakeIncludes = true
		c, err := newCPP(cfg, newFset(), []Source{
			{Name: "<predefined>", Value: predef},
			{Name: "test.c", Value: src},
		}, nil)
		if err != nil {
			return "", err
		}
		var b strings.Builder
		if err := preprocess(c, &b); err != nil {
			return "", err
		}
		return b.String(), nil
	}

	// Leopard-shaped TU: no __ATOMIC_RELAXED predefined, no user macro kicked
	// in by the TU itself.
	out, err := newPP("")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "lacks_c_atomic") || strings.Contains(out, "has_c_atomic") {
		t.Fatalf("no __ATOMIC_RELAXED predefined: __has_extension(c_atomic) must be false, got:\n%v", out)
	}

	// A CC with C11 atomics (every upstream modernc target's -dM output)
	// provides the marker.
	out, err = newPP("#define __ATOMIC_RELAXED 2\n")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "has_c_atomic") || strings.Contains(out, "lacks_c_atomic") {
		t.Fatalf("__ATOMIC_RELAXED predefined: __has_extension(c_atomic) must be true, got:\n%v", out)
	}
}
