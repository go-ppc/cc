// Copyright 2021 The CC Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package cc // import "modernc.org/cc/v4"

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/fstest"

	"github.com/dustin/go-humanize"
	"github.com/pbnjay/memory"
	"github.com/pmezard/go-difflib/difflib"
	"modernc.org/ccorpus2"
	"modernc.org/mathutil"
)

var (
	cfs         = ccorpus2.FS
	corpusIndex []string
	defaultCfg0 *Config
	re          *regexp.Regexp
	totalRam    = memory.TotalMemory()
	makeJ       = fmt.Sprintf("-j%d", runtime.GOMAXPROCS(-1))
	_           = stack
	goos        = runtime.GOOS
	goarch      = runtime.GOARCH
	target      = fmt.Sprintf("%s/%s", goos, goarch)

	oTrace = flag.Bool("trc", false, "Print tested paths.")
)

func init() {
	flag.BoolVar(&traceFails, "trcfails", false, "")
	isTesting = true
	extendedErrors = true
	var err error
	flags := []string{LongDouble64Flag(runtime.GOOS, runtime.GOARCH)}
	if goos == "windows" && (goarch == "386" || goarch == "amd64") {
		flags = append(flags,
			"-mno-3dnow",
			"-mno-abm",
			"-mno-aes",
			"-mno-avx",
			"-mno-avx2",
			"-mno-avx512cd",
			"-mno-avx512er",
			"-mno-avx512f",
			"-mno-avx512pf",
			"-mno-bmi",
			"-mno-bmi2",
			"-mno-f16c",
			"-mno-fma",
			"-mno-fma4",
			"-mno-fsgsbase",
			"-mno-lwp",
			"-mno-lzcnt",
			"-mno-mmx",
			"-mno-pclmul",
			"-mno-popcnt",
			"-mno-prefetchwt1",
			"-mno-rdrnd",
			"-mno-sha",
			"-mno-sse",
			"-mno-sse2",
			"-mno-sse3",
			"-mno-sse4",
			"-mno-sse4.1",
			"-mno-sse4.2",
			"-mno-sse4a",
			"-mno-ssse3",
			"-mno-tbm",
			"-mno-xop",
		)
	}
	if defaultCfg0, err = NewConfig(runtime.GOOS, runtime.GOARCH, flags...); err != nil {
		panic(errorf("NewConfig: %v", err))
	}

	if err := walk("assets", func(pth string, fi os.FileInfo) error {
		corpusIndex = append(corpusIndex, pth)
		return nil
	}); err != nil {
		panic(err)
	}

}

func walk(dir string, f func(pth string, fi os.FileInfo) error) error {
	fis, err := cfs.ReadDir(dir)
	if err != nil {
		return err
	}

	for _, v := range fis {
		switch {
		case v.IsDir():
			if err = walk(dir+"/"+v.Name(), f); err != nil {
				return err
			}
		default:
			fi, err := v.Info()
			if err != nil {
				return err
			}

			if err = f(dir+"/"+v.Name(), fi); err != nil {
				return err
			}
		}
	}
	return nil
}

// Produce the AST used in examples documentation.
func exampleAST(rule int, src string) (r interface{}) {
	defer func() {
		if err := recover(); err != nil {
			r = fmt.Sprintf("%v (%v:)", err, origin(5))
		}
	}()

	src = strings.Replace(src, "\\n", "\n", -1)
	cfg := &Config{}
	ast, _ := Parse(cfg, []Source{{Name: "example.c", Value: src}})
	if ast == nil {
		return "FAIL"
	}

	pc, _, _, _ := runtime.Caller(1)
	typ := runtime.FuncForPC(pc - 1).Name()
	i := strings.LastIndexByte(typ, '.')
	typ = typ[i+1+len("Example"):]
	i = strings.LastIndexByte(typ, '_')
	typ = typ[:i]
	var node Node
	depth := mathutil.MaxInt
	findNode(typ, ast.TranslationUnit, 0, &node, &depth)
	return node
}

func TestMain(m *testing.M) {
	oRE := flag.String("re", "", "")
	flag.Parse()
	if *oRE != "" {
		re = regexp.MustCompile(*oRE)
	}
	os.Exit(m.Run())
}

func TestScannerSource(t *testing.T) {
	const fn = "all_test.go"
	exp, err := os.ReadFile(fn)
	if err != nil {
		t.Fatal(err)
	}

	f, err := os.Open(fn)
	if err != nil {
		t.Fatal(err)
	}

	testScannerSource(t, fn, f, exp, false)
	testScannerSource(t, fn, exp, exp, false)
	testScannerSource(t, fn, string(exp), exp, false)
	testScannerSource(t, fn, bytes.NewReader(exp), exp, false)
	testScannerSource(t, fn, nil, exp, false)
	testScannerSource(t, fn, 42, nil, true)
}

func testScannerSource(t *testing.T, name string, value interface{}, exp []byte, mustFail bool) {
	ss, err := newScannerSource(newFset(), Source{name, value, nil})
	if err != nil != mustFail {
		t.Fatalf("(%q, %T): %v", name, value, err)
	}

	if err != nil {
		return
	}

	if !bytes.Equal(ss.buf, exp) {
		t.Fatal("buf does not match")
	}
}

func TestToken(t *testing.T) {
	s, err := newScannerSource(newFset(), Source{"test", `abc
def
 ghi
`, nil})
	// abc\ndef\n ghi\n
	//             1
	// 0123 4567 89012
	if err != nil {
		t.Fatal(err)
	}

	s.file.AddLine(4)
	s.file.AddLine(8)
	s.file.AddLine(13)
	for itest, test := range []struct {
		Token
		line int
		col  int
		ch   rune
		sep  string
		src  string
	}{
		{newToken(s, 0, 0, 0, 3), 1, 1, 0, "", "abc"},
		{newToken(s, 1, 3, 4, 3), 2, 1, 1, "\n", "def"},
		{newToken(s, 2, 7, 9, 3), 3, 2, 2, "\n ", "ghi"},
		{newToken(s, eof, 13, 13, 0), 3, 6, eof, "", ""},
	} {
		tok := test.Token
		if g, e := tok.Position().Line, test.line; g != e {
			t.Fatal(itest, g, e)
		}
		if g, e := tok.Position().Column, test.col; g != e {
			t.Fatal(itest, g, e)
		}
		if g, e := tok.Ch, test.ch; g != e {
			t.Fatal(itest, g, e)
		}
		if g, e := string(tok.Sep()), test.sep; g != e {
			t.Fatalf("%v %q %q", itest, g, e)
		}
		if g, e := tok.SrcStr(), test.src; g != e {
			t.Fatalf("%v %q %q", itest, g, e)
		}

		tok2 := tok
		tok2.Set([]byte("xyz0123"), []byte("456789"))
		if g, e := string(tok.Sep()), test.sep; g != e {
			t.Fatalf("%v %q %q", itest, g, e)
		}
		if g, e := tok.SrcStr(), test.src; g != e {
			t.Fatalf("%v %q %q", itest, g, e)
		}
		if g, e := string(tok2.Sep()), "xyz0123"; g != e {
			t.Fatalf("%v %q %q", itest, g, e)
		}
		if g, e := tok2.SrcStr(), "456789"; g != e {
			t.Fatalf("%v %q %q", itest, g, e)
		}
	}
}

func TestPragmaOnceSkipSource(t *testing.T) {
	dir := t.TempDir()
	header := filepath.Join(dir, "test.h")
	main := filepath.Join(dir, "test2.h")
	if err := os.WriteFile(header, []byte(`#pragma once

#ifdef TESTPRAGMA
#error pragma once not working
#endif
#define TESTPRAGMA
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(main, []byte(`#include "test.h"
#include "test.h"
`), 0o644); err != nil {
		t.Fatal(err)
	}

	seen := map[string]struct{}{}
	cfg := &Config{
		IncludePaths: []string{"@"},
		PragmaHandler: func(toks []Token) error {
			if len(toks) == 0 || toks[0].SrcStr() != "once" {
				return nil
			}

			fn := toks[0].Position().Filename
			if _, ok := seen[fn]; ok {
				return SkipSource
			}

			seen[fn] = struct{}{}
			return nil
		},
	}

	if err := Preprocess(cfg, []Source{{Name: main}}, io.Discard); err != nil {
		t.Fatalf("Preprocess: %v", err)
	}
}

type parallel struct {
	limit chan struct{}
	sync.Mutex
	wg sync.WaitGroup
}

func newParallel() *parallel {
	limit := runtime.GOMAXPROCS(-1)
	switch runtime.GOARCH {
	case "386", "arm": // 32 bit targets
		limit = 1
	default:
		if totalRam <= 1<<32 {
			limit = 1
		}
	}
	return &parallel{
		limit: make(chan struct{}, limit),
	}
}

func (p *parallel) exec(run func()) {
	p.wg.Add(1)
	p.limit <- struct{}{}

	go func() {
		defer func() {
			p.wg.Done()
			<-p.limit
		}()

		run()
	}()
}

var tokSink []Token

func TestScanner(t *testing.T) {
	defer func() { tokSink = nil }()

	var files, tokens, chars int64
	var m0, m runtime.MemStats
	p := newParallel()
	debug.FreeOSMemory()
	runtime.ReadMemStats(&m0)
	for _, path := range corpusIndex {
		path := path
		switch filepath.Ext(path) {
		case ".c", ".h":
			files++
			p.exec(func() {
				var err error
				var chars0, tokens0 int64
				var toks []Token

				defer func() {
					p.Lock()
					chars += chars0
					tokens += tokens0
					tokSink = append(tokSink, toks...)
					if err != nil {
						t.Error(err)
					}
					p.Unlock()
				}()

				var buf []byte
				if buf, err = getCorpusFile(path); err != nil {
					return
				}

				chars0 += int64(len(buf))
				var s *scanner
				if s, err = newScanner(newFset(), Source{path, buf, nil}, func(msg string, args ...interface{}) {
					s.close()
					err = fmt.Errorf(msg, args...)
				}); err != nil {
					err = fmt.Errorf("%v: %v", path, err)
					return
				}

				for {
					tok := s.cppScan()
					if tok.Ch == eof {
						return
					}

					toks = append(toks, tok)
					tokens0++
				}
			})
		}
	}
	p.wg.Wait()
	debug.FreeOSMemory()
	runtime.ReadMemStats(&m)
	t.Logf("files %v; tokens %v; bytes %v; heap %v; alloc %v", h(files), h(tokens), h(chars), h(m.HeapAlloc-m0.HeapAlloc), h(m.TotalAlloc-m0.TotalAlloc))
}

func getCorpusFile(path string) ([]byte, error) {
	f, err := cfs.Open(path)
	if err != nil {
		return nil, err
	}

	return io.ReadAll(f)
}

func h(v interface{}) string {
	switch x := v.(type) {
	case int64:
		return humanize.Comma(x)
	case uint64:
		if x <= math.MaxInt64 {
			return humanize.Comma(int64(x))
		}
	}
	return fmt.Sprint(v)
}

func BenchmarkScanner(b *testing.B) {
	debug.FreeOSMemory()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var chars int64
		for _, path := range corpusIndex {
			switch filepath.Ext(path) {
			case ".c", ".h":
				buf, err := getCorpusFile(path)
				if err != nil {
					b.Fatal(err)
				}

				chars += int64(len(buf))
				var s *scanner
				if s, err = newScanner(newFset(), Source{path, buf, nil}, func(msg string, args ...interface{}) {
					s.close()
					b.Fatalf(msg, args...)
				}); err != nil {
					b.Fatal(path, err)
				}
				for {
					tok := s.cppScan()
					if tok.Ch == eof {
						break
					}
				}
			}
		}
		b.SetBytes(chars)
	}
}

var (
	cppParseBlacklist = map[string]struct{}{
		"assets/github.com/vnmakarov/mir/c-tests/new/endif.c": {}, // 1:1: unexpected #endif
	}
	astSink []group
)

func TestCPPParse0(t *testing.T) {
	defer func() { astSink = nil }()

	var files, lines, chars int64
	var m0, m runtime.MemStats
	debug.FreeOSMemory()
	runtime.ReadMemStats(&m0)
	for _, path := range corpusIndex {
		if _, ok := cppParseBlacklist[path]; ok {
			continue
		}

		switch filepath.Ext(path) {
		case ".c", ".h":
			buf, err := getCorpusFile(path)
			if err != nil {
				t.Fatal(path, err)
			}

			chars += int64(len(buf))
			var p *cppParser
			if p, err = newCppParser(newFset(), Source{path, buf, nil}, func(msg string, args ...interface{}) {
				p.close()
				t.Fatalf(msg, args...)
			}); err != nil {
				t.Fatal(path, err)
			}

			files++
			ast := p.preprocessingFile()
			if len(ast) == 0 {
				t.Fatalf("%v: empty AST", path)
			}

			eol := ast[len(ast)-1]
			x, ok := eol.(eofLine)
			if !ok {
				t.Fatalf("%v: AST not terminated: %T", p.pos(), eol)
			}

			eof := Token(x)
			lines += int64(eof.Position().Line)
			astSink = append(astSink, ast)
		}
	}
	debug.FreeOSMemory()
	runtime.ReadMemStats(&m)
	astSink = nil
	t.Logf("files %v; lines %v bytes %v; heap %v; alloc %v", h(files), h(lines), h(chars), h(m.HeapAlloc-m0.HeapAlloc), h(m.TotalAlloc-m0.TotalAlloc))
}

func TestCPPParse(t *testing.T) {
	defer func() { astSink = nil }()

	var files, lines, chars int64
	var m0, m runtime.MemStats
	p := newParallel()
	debug.FreeOSMemory()
	runtime.ReadMemStats(&m0)
	for _, path := range corpusIndex {
		path := path
		if _, ok := cppParseBlacklist[path]; ok {
			continue
		}

		switch filepath.Ext(path) {
		case ".c", ".h":
			files++
			p.exec(func() {
				buf, err := getCorpusFile(path)
				if err != nil {
					t.Fatal(path, err)
				}

				var ast group
				var eof Token

				defer func() {
					p.Lock()
					chars += int64(len(buf))
					lines += int64(eof.Position().Line)
					astSink = append(astSink, ast)
					if err != nil {
						t.Error(err)
					}
					p.Unlock()
				}()

				var p *cppParser
				if p, err = newCppParser(newFset(), Source{path, buf, nil}, func(msg string, args ...interface{}) {
					p.close()
					err = fmt.Errorf(msg, args...)
				}); err != nil {
					t.Fatal(path, err)
				}

				if ast = p.preprocessingFile(); len(ast) == 0 {
					t.Fatalf("%v: empty AST", path)
				}

				eol := ast[len(ast)-1]
				x, ok := eol.(eofLine)
				if !ok {
					err = fmt.Errorf("%v: AST not terminated: %T", p.pos(), eol)
					return
				}

				eof = Token(x)
			})
		}
	}
	p.wg.Wait()
	debug.FreeOSMemory()
	runtime.ReadMemStats(&m)
	astSink = nil
	t.Logf("files %v; lines %v bytes %v; heap %v; alloc %v", h(files), h(lines), h(chars), h(m.HeapAlloc-m0.HeapAlloc), h(m.TotalAlloc-m0.TotalAlloc))
}

func BenchmarkCPPParse(b *testing.B) {
	debug.FreeOSMemory()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var chars int64
		for _, path := range corpusIndex {
			if _, ok := cppParseBlacklist[path]; ok {
				continue
			}

			switch filepath.Ext(path) {
			case ".c", ".h":
				buf, err := getCorpusFile(path)
				if err != nil {
					b.Fatal(err)
				}

				chars += int64(len(buf))
				var p *cppParser
				if p, err = newCppParser(newFset(), Source{path, buf, nil}, func(msg string, args ...interface{}) {
					p.close()
					b.Fatalf(msg, args...)
				}); err != nil {
					b.Fatal(path, err)
				}

				ast := p.preprocessingFile()
				if len(ast) == 0 {
					b.Fatalf("%v: empty AST", path)
				}

				eol := ast[len(ast)-1]
				if _, ok := eol.(eofLine); !ok {
					b.Fatalf("%v: AST not terminated: %T", p.pos(), eol)
				}
			}
		}
		b.SetBytes(chars)
	}
}

func defaultCfg() *Config {
	c := *defaultCfg0
	return &c
}

func TestCPPExpand(t *testing.T) {
	testCPPExpand(t, "testdata/cpp-expand/", nil, true)
}

func testCPPExpand(t *testing.T, dir string, blacklist map[string]struct{}, fakeIncludes bool) {
	var fails []string
	var files, ok, skip int
	var c *cpp
	cfg := defaultCfg()
	cfg.fakeIncludes = fakeIncludes
	cfg.PragmaHandler = func(s []Token) error {
		pragmaTestTok := Token{s: s[0].s, Ch: rune(IDENTIFIER)}
		pragmaTestTok.Set(nil, []byte("__pragma"))
		a := textLine{pragmaTestTok}
		for i, v := range s {
			if i == 0 {
				v.Set(sp, v.Src())
			}
			a = append(a, v)
		}
		nlTok := Token{s: s[0].s, Ch: '\n'}
		nlTok.Set(nil, nl)
		c.push(append(a, nlTok))
		return nil
	}
	err := filepath.Walk(filepath.FromSlash(dir), func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		if info.IsDir() || (!strings.HasSuffix(path, ".c") && !strings.HasSuffix(path, ".h")) {
			return nil
		}

		files++
		switch {
		case re != nil:
			if !re.MatchString(path) {
				skip++
				return nil
			}
		default:
			if _, ok := blacklist[filepath.Base(path)]; ok {
				skip++
				return nil
			}
		}

		if *oTrace {
			fmt.Fprintln(os.Stderr, path)
		}
		var b strings.Builder
		if c, err = newCPP(cfg, newFset(), []Source{{path, nil, nil}}, nil); err != nil {
			t.Fatalf("%v: %v", path, err)
		}

		if err := preprocess(c, &b); err != nil {
			fails = append(fails, path)
			t.Fatalf("%v: %v", path, err)
		}

		if strings.Contains(filepath.ToSlash(path), "/mustfail/") {
			if err != nil {
				return nil
			}

			fails = append(fails, path)
			return fmt.Errorf("%v: unexpected success", path)
		}

		if err != nil {
			fails = append(fails, path)
			return err
		}

		expFn := path + ".expect"
		exp, err := os.ReadFile(expFn)
		if err != nil {
			fails = append(fails, path)
			t.Error(err)
		}

		g := strings.ReplaceAll(b.String(), "\r", "")
		g = strings.TrimSpace(g)
		e := strings.ReplaceAll(string(exp), "\r", "")
		e = strings.TrimSpace(e)
		if g != e {
			fails = append(fails, path)
			diff := difflib.UnifiedDiff{
				A:        difflib.SplitLines(e),
				B:        difflib.SplitLines(g),
				FromFile: expFn,
				ToFile:   path,
				Context:  0,
			}
			s, err := difflib.GetUnifiedDiffString(diff)
			if err != nil {
				t.Fatalf("%v: %v", path, err)
			}

			t.Errorf("%v\ngot\n%s\nexp\n%s\ngot\n%s\nexp\n%s", s, g, e, hex.Dump([]byte(g)), hex.Dump([]byte(e)))
			return nil
		}
		ok++
		return nil
	})
	for _, v := range fails {
		t.Log(v)
	}
	t.Logf("files %v, skip %v, ok %v, fails %v", files, skip, ok, len(fails))
	if err != nil {
		t.Fatal(err)
	}
}

func TestPreprocess(t *testing.T) {
	testCPPExpand(t, "testdata/preprocess/", nil, true)
}

func TestTCCExpand(t *testing.T) {
	testCPPExpand(t, "testdata/tcc-0.9.27/tests/pp/", map[string]struct{}{
		"11.c": {}, // https://gcc.gnu.org/onlinedocs/gcc/Variadic-Macros.html#Variadic-Macros
		"16.c": {}, // We don't produce warnings on macro redefinition.
	}, true)
}

func TestInclude(t *testing.T) {
	testCPPExpand(t, "testdata/include/", nil, false)
}

func TestTranslationPhase4(t *testing.T) {
	cfg := defaultCfg()
	cfg.SysIncludePaths = append(cfg.SysIncludePaths, "Include") // benchmarksgame
	cfg.FS = cfs
	blacklistCompCert := map[string]struct{}{}
	blacklistGCC := map[string]struct{}{
		// assertions are deprecated, not supported.
		"950919-1.c": {},
	}
	blacklictTCC := map[string]struct{}{
		// https://gcc.gnu.org/onlinedocs/gcc/Variadic-Macros.html#Variadic-Macros, not supported.
		"11.c": {},
	}
	switch fmt.Sprintf("%s/%s", runtime.GOOS, runtime.GOARCH) {
	case "linux/s390x", "linux/loong64", "linux/mips64le":
		blacklistCompCert["aes.c"] = struct{}{} // Unsupported endianness.
	}
	var files, ok, skip, fails int32
	for _, v := range []struct {
		cfg       *Config
		dir       string
		blacklist map[string]struct{}
	}{
		{cfg, "CompCert-3.6/test/c", blacklistCompCert},
		{cfg, "ccgo", nil},
		{cfg, "gcc-9.1.0/gcc/testsuite/gcc.c-torture", blacklistGCC},
		{cfg, "github.com/AbsInt/CompCert/test/c", blacklistCompCert},
		{cfg, "github.com/cxgo", nil},
		{cfg, "github.com/gcc-mirror/gcc/gcc/testsuite", blacklistGCC},
		{cfg, "github.com/vnmakarov", nil},
		{cfg, "sqlite-amalgamation", nil},
		{cfg, "tcc-0.9.27/tests", blacklictTCC},
		{cfg, "benchmarksgame-team.pages.debian.net", nil},
	} {
		t.Run(v.dir, func(t *testing.T) {
			f, o, s, n := testTranslationPhase4(t, v.cfg, "assets/"+v.dir, v.blacklist)
			files += f
			ok += o
			skip += s
			fails += n
		})
	}
	t.Logf("TOTAL: files %v, skip %v, ok %v, fails %v", files, skip, ok, fails)
}

func testTranslationPhase4(t *testing.T, cfg *Config, dir string, blacklist map[string]struct{}) (files, ok, skip, nfails int32) {
	tmp := t.TempDir()
	var fails []string
	p := newParallel()
	err := walk(dir, func(pth string, fi os.FileInfo) error {
		if fi.IsDir() {
			return nil
		}

		if filepath.Ext(pth) != ".c" {
			return nil
		}

		switch {
		case re != nil:
			if !re.MatchString(pth) {
				atomic.AddInt32(&skip, 1)
				return nil
			}
		default:
			if _, ok := blacklist[filepath.Base(pth)]; ok {
				atomic.AddInt32(&skip, 1)
				return nil
			}
		}

		files++
		apth := pth
		p.exec(func() {
			if *oTrace {
				fmt.Fprintln(os.Stderr, apth)
			}
			var err error

			defer func() {
				p.Lock()

				defer p.Unlock()

				if err != nil {
					fails = append(fails, apth)
					t.Errorf("%v: %v", apth, err)
				}
			}()

			if err = Preprocess(
				cfg,
				[]Source{
					{Name: "<predefined>", Value: cfg.Predefined},
					{Name: "<builtin>", Value: Builtin},
					{Name: apth, FS: cfs},
				},
				io.Discard,
			); err == nil {
				atomic.AddInt32(&ok, 1)
				return
			}

			f, err2 := cfs.Open(apth)
			if err2 != nil {
				err = errorf("", err2)
				return
			}

			defer f.Close()

			b := make([]byte, fi.Size())
			if n, _ := f.Read(b); int64(n) != fi.Size() {
				err = errorf("%v: short read", apth)
				return
			}

			fn := filepath.Join(tmp, filepath.Base(apth))
			if err2 := os.WriteFile(fn, b, 0660); err2 != nil {
				err = errorf("", err2)
				return
			}

			defer os.Remove(fn)

			cmd := exec.Command(cfg.CC, "-E", fn)
			var buf bytes.Buffer
			cmd.Stderr = &buf
			if err2 = cmd.Run(); err2 != nil {
				t.Logf("%v: skip: %v: %s %v", apth, cfg.CC, buf.Bytes(), err2)
				atomic.AddInt32(&skip, 1)
				err = nil
			}
		})
		return nil
	})
	if err != nil {
		t.Error(err)
	}

	p.wg.Wait()
	for _, v := range fails {
		t.Log(v)
	}
	t.Logf("files %v, skip %v, ok %v, fails %v", files, skip, ok, len(fails))
	return files, ok, skip, int32(len(fails))
}

// https://gitlab.com/cznic/cc/-/issues/127
func TestIssue127(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}

	defer func() {
		if err := os.Chdir(wd); err != nil {
			t.Fatal(err)
		}
	}()

	if err := os.Chdir(filepath.FromSlash("testdata/issue127/")); err != nil {
		t.Error(err)
		return
	}

	cd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}

	t.Logf("working directory: %s", cd)
	cfg := defaultCfg()
	cfg.IncludePaths = append(cfg.IncludePaths, "include")
	if err := Preprocess(
		cfg,
		[]Source{{Name: "main.c"}},
		io.Discard,
	); err != nil {
		t.Fatalf("%v", err)
	}
}

func TestBOM(t *testing.T) {
	for i, v := range []struct {
		src string
		err string
	}{
		{"int main() {}", ""},
		{"\xEF\xBB\xBFint main() {}", ""},
	} {
		cfg := *defaultCfg()
		cfg.noPredefinedDeclarator = true
		switch _, err := Parse(&cfg, []Source{{Value: v.src}}); {
		case v.err == "" && err != nil:
			t.Errorf("%v: unexpected error %v", i, err)
		case v.err != "" && err == nil:
			t.Errorf("%v: unexpected success, expected error matching %v", i, v.err)
		case v.err != "":
			if !regexp.MustCompile(v.err).MatchString(err.Error()) {
				t.Errorf("%v: error %v does not match %v", i, err, v.err)
			}
		}
	}
}

func TestStrCatSep(t *testing.T) {
	for i, v := range []struct {
		src         string
		lit         string
		sep         string
		trailingSep string
	}{
		{`int f() {  "a";}`, `"a"`, "  ", "\n"},
		{`int f() {  L"a";}`, `L"a"`, "  ", "\n"},
		{`int f() { "a" "b";}`, `"ab"`, "  ", "\n"},
		{`int f() { "a""b";}`, `"ab"`, " ", "\n"},
		{`int f() { "a";}`, `"a"`, " ", "\n"},

		{`int f() { "a"` + "\n\t" + `"b"; }`, `"ab"`, " \n\t", "\n"},
		{`int f() { /*x*/ /*y*/ "a";}`, `"a"`, " /*x*/ /*y*/ ", "\n"},
		{`int f() { /*x*/` + "\n" + `/*y*/ "a";}`, `"a"`, " /*x*/\n/*y*/ ", "\n"},
		{`int f() { //x` + "\n" + ` "a";}`, `"a"`, " //x\n ", "\n"},
		{`int f() { //x` + "\n" + `"a";}`, `"a"`, " //x\n", "\n"},

		{`int f() { L"a" L"b";}`, `L"ab"`, "  ", "\n"},
		{`int f() { ` + "\n" + ` "a";}`, `"a"`, " \n ", "\n"},
		{`int f() { ` + "\n" + `"a";}`, `"a"`, " \n", "\n"},
		{`int f() {"a" "b";}`, `"ab"`, " ", "\n"},
		{`int f() {"a"/*y*/"b";}`, `"ab"`, "/*y*/", "\n"},

		{`int f() {"a";} /*x*/ `, `"a"`, "", " /*x*/ \n"},
		{`int f() {"a";} /*x*/`, `"a"`, "", " /*x*/\n"},
		{`int f() {"a";} /*x` + "\n" + `*/ `, `"a"`, "", " /*x\n*/ \n"},
		{`int f() {"a";} `, `"a"`, "", " \n"},
		{`int f() {"a";}/*x*/`, `"a"`, "", "/*x*/\n"},

		{`int f() {"a";}` + "\n", `"a"`, "", "\n"},
		{`int f() {"a";}`, `"a"`, "", "\n"},
		{`int f() {/*x*/ /*y*/ "a";}`, `"a"`, "/*x*/ /*y*/ ", "\n"},
		{`int f() {/*x*/"a""b";}`, `"ab"`, "/*x*/", "\n"},
		{`int f() {/*x*/"a"/*y*/"b";}`, `"ab"`, "/*x*//*y*/", "\n"},

		{`int f() {/*x*/"a";}`, `"a"`, "/*x*/", "\n"},
		{`int f() {/*x*//*y*/ "a";}`, `"a"`, "/*x*//*y*/ ", "\n"},
		{`int f() {/*x*//*y*/"a";}`, `"a"`, "/*x*//*y*/", "\n"},
		{`int f() {//` + "\n" + `"a";}`, `"a"`, "//\n", "\n"},
		{`int f() {//x` + "\n" + `"a";}`, `"a"`, "//x\n", "\n"},

		{`int f() {` + "/*A*/\n/*B*/\n" + `"a";}`, `"a"`, "/*A*/\n/*B*/\n", "\n"},
		{`int f() {` + "\n" + ` "a";}`, `"a"`, "\n ", "\n"},
		{`int f() {` + "\n" + `"a";}`, `"a"`, "\n", "\n"},
	} {
		ast, err := Parse(
			&Config{
				doNotInjectFunc:        true,
				noPredefinedDeclarator: true,
			},
			[]Source{{Name: "test", Value: v.src}},
		)
		if err != nil {
			t.Errorf("%v: %v", i, err)
			continue
		}

		// trc("%q -> %q", v.src, nodeSource2(ast.TranslationUnit))
		var n Node
		depth := mathutil.MaxInt
		findNode("PrimaryExpression", ast.TranslationUnit, 0, &n, &depth)
		tok := n.(*PrimaryExpression).Token
		if g, e := tok.SrcStr(), v.lit; g != e {
			t.Errorf("%v: %q %q", i, g, e)
		}
		if g, e := string(tok.Sep()), v.sep; g != e {
			t.Errorf("%v: %q %q", i, g, e)
		}
		if g, e := string(ast.EOF.Sep()), v.trailingSep; g != e {
			t.Errorf("%v: %q %q", i, g, e)
		}
	}
}

func TestParserBug(t *testing.T) {
	blacklistJourdan := map[string]struct{}{
		// Type checking has to detect the fail.
		"bitfield_declaration_ambiguity.fail.c": {},
	}
	t.Run("bug", func(t *testing.T) { testParserBug(t, "testdata/bug", nil) })
	t.Run("jhjourdan", func(t *testing.T) { testParserBug(t, "testdata/jhjourdan", blacklistJourdan) })
}

func testParserBug(t *testing.T, dir string, blacklist map[string]struct{}) {
	tmp := t.TempDir()
	cfg := defaultCfg()
	var fails []string
	var files, ok, skip int
	err := filepath.Walk(filepath.FromSlash(dir), func(pth string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		if fi.IsDir() {
			return nil
		}

		if filepath.Ext(pth) != ".c" {
			return nil
		}

		switch {
		case re != nil:
			if !re.MatchString(pth) {
				skip++
				return nil
			}
		}

		files++
		switch {
		case re != nil:
			if !re.MatchString(pth) {
				skip++
				return nil
			}
		default:
			if _, ok := blacklist[filepath.Base(pth)]; ok {
				skip++
				return nil
			}
		}

		if *oTrace {
			fmt.Fprintln(os.Stderr, pth)
		}

		sources := []Source{
			{Name: "<predefined>", Value: cfg.Predefined},
			{Name: "<builtin>", Value: Builtin},
			{Name: pth},
		}
		_, err = Parse(cfg, sources)
		switch {
		case strings.Contains(pth, ".fail."):
			if err == nil {
				fails = append(fails, pth)
				t.Errorf("%v: missing error", pth)
			} else {
				if *oTrace {
					t.Log(err)
				}
				ok++
			}
		case err == nil:
			ok++
		default:
			cmd := exec.Command(cfg.CC, "-c", "-o", filepath.Join(tmp, "test.o"), pth)
			var buf bytes.Buffer
			cmd.Stderr = &buf
			if err2 := cmd.Run(); err2 != nil {
				t.Logf("%v: skip: %v: %s %v", pth, cfg.CC, buf.Bytes(), err2)
				skip++
				break
			}

			fails = append(fails, pth)
			t.Errorf("%v: %v", pth, err)
		}
		return nil
	})
	if err != nil {
		t.Error(err)
	}

	for _, v := range fails {
		t.Log(v)
	}
	t.Logf("files %v, skip %v, ok %v, fails %v", files, skip, ok, len(fails))
}

// ~/src/modernc.org/ccorpus2/

func TestParse(t *testing.T) {
	cfg := defaultCfg()
	cfg.SysIncludePaths = append(cfg.SysIncludePaths, "Include") // benchmarksgame
	cfg.FS = cfs
	blacklistCompCert := map[string]struct{}{}
	blacklistGCC := map[string]struct{}{
		// Assertions are deprecated, not supported.
		"950919-1.c": {},
	}
	blacklistDebian := map[string]struct{}{
		"nbody-4.c": {}, // _Float16 not supported
		"nbody-8.c": {}, // _Float16 not supported
	}
	switch fmt.Sprintf("%s/%s", runtime.GOOS, runtime.GOARCH) {
	case "linux/s390x", "linux/loong64", "linux/mips64le":
		blacklistCompCert["aes.c"] = struct{}{} // Unsupported endianness.
	case "freebsd/amd64":
		blacklistDebian["mandelbrot-6.c"] = struct{}{}
		blacklistDebian["spectral-norm-5.c"] = struct{}{}
	case "darwin/amd64":
		blacklistDebian["fannkuchredux-4.c"] = struct{}{}
		blacklistDebian["mandelbrot-6.c"] = struct{}{}
	}
	type bm map[string]struct{}
	var files, ok, skip, fails int32
	for _, v := range []struct {
		cfg             *Config
		dir             string
		blacklist       map[string]struct{}
		targetBlacklist map[string]struct{}
	}{
		{cfg, "CompCert-3.6/test/c", blacklistCompCert, nil},
		{cfg, "ccgo", nil, nil},
		{cfg, "gcc-9.1.0/gcc/testsuite/gcc.c-torture", blacklistGCC, nil},
		{cfg, "github.com/AbsInt/CompCert/test/c", blacklistCompCert, nil},
		{cfg, "github.com/cxgo", nil, nil},
		{cfg, "github.com/gcc-mirror/gcc/gcc/testsuite", blacklistGCC, nil},
		{cfg, "github.com/vnmakarov", nil, nil},
		{cfg, "sqlite-amalgamation", nil, bm{"windows/amd64": {}}},
		{cfg, "tcc-0.9.27/tests/tests2", nil, nil},
		{cfg, "benchmarksgame-team.pages.debian.net", blacklistDebian, nil},
	} {
		if _, ok := v.targetBlacklist[target]; ok {
			continue
		}

		t.Run(v.dir, func(t *testing.T) {
			f, o, s, n := testParse(t, v.cfg, "assets/"+v.dir, v.blacklist)
			files += f
			ok += o
			skip += s
			fails += n
		})
	}
	t.Logf("TOTAL: files %v, skip %v, ok %v, fails %v", files, skip, ok, fails)
}

func testParse(t *testing.T, cfg *Config, dir string, blacklist map[string]struct{}) (files, ok, skip, nfails int32) {
	tmp := t.TempDir()
	var fails []string
	p := newParallel()
	err := walk(dir, func(pth string, fi os.FileInfo) error {
		if fi.IsDir() {
			return nil
		}

		if filepath.Ext(pth) != ".c" {
			return nil
		}

		files++
		switch {
		case re != nil:
			if !re.MatchString(pth) {
				atomic.AddInt32(&skip, 1)
				return nil
			}
		default:
			if _, ok := blacklist[filepath.Base(pth)]; ok {
				atomic.AddInt32(&skip, 1)
				return nil
			}
		}
		apth := pth
		p.exec(func() {
			if *oTrace {
				fmt.Fprintln(os.Stderr, apth)
			}

			var err error

			defer func() {
				p.Lock()

				defer p.Unlock()

				if err != nil {
					fails = append(fails, apth)
					t.Errorf("%v: %v", apth, err)
				}

			}()

			func() {
				defer func() {
					if e := recover(); e != nil && err == nil {
						err = fmt.Errorf("%v: PANIC: %v", apth, e)
						// trc("\n%s", debug.Stack())
					}
				}()

				if _, err = Parse(
					cfg,
					[]Source{
						{Name: "<predefined>", Value: cfg.Predefined},
						{Name: "<builtin>", Value: Builtin},
						{Name: apth, FS: cfs},
					},
				); err == nil {
					atomic.AddInt32(&ok, 1)
					return
				}
			}()

			if err == nil {
				return
			}

			f, err2 := cfs.Open(apth)
			if err2 != nil {
				err = errorf("", err2)
				return
			}

			defer f.Close()

			b := make([]byte, fi.Size())
			if n, _ := f.Read(b); int64(n) != fi.Size() {
				err = errorf("%v: short read", apth)
				return
			}

			fn := filepath.Join(tmp, filepath.Base(apth))
			if err2 := os.WriteFile(fn, b, 0660); err2 != nil {
				err = errorf("", err2)
				return
			}

			defer os.Remove(fn)

			cmd := exec.Command(cfg.CC, "-c", "-o", filepath.Join(tmp, "test.o"), fn)
			var buf bytes.Buffer
			cmd.Stderr = &buf
			if err2 = cmd.Run(); err2 != nil {
				t.Logf("%v: skip: %v: %s %v", apth, cfg.CC, buf.Bytes(), err2)
				atomic.AddInt32(&skip, 1)
				err = nil
				return
			}
		})
		return nil
	})
	if err != nil {
		t.Error(err)
	}

	p.wg.Wait()
	for _, v := range fails {
		t.Log(v)
	}
	// fmt.Fprintf(os.Stderr, "%v: files %v, skip %v, ok %v, fails %v\n", dir, files, skip, ok, len(fails))
	t.Logf("files %v, skip %v, ok %v, fails %v", files, skip, ok, len(fails))
	return files, ok, skip, int32(len(fails))
}

func TestTranslateBug(t *testing.T) {
	t.Run("bug", func(t *testing.T) { testTranslateBug(t, "testdata/bug", nil) })
}

func testTranslateBug(t *testing.T, dir string, blacklist map[string]struct{}) {
	tmp := t.TempDir()
	cfg := defaultCfg()
	var fails []string
	var files, ok, skip int
	err := filepath.Walk(filepath.FromSlash(dir), func(pth string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		if fi.IsDir() {
			return nil
		}

		if filepath.Ext(pth) != ".c" {
			return nil
		}

		switch {
		case re != nil:
			if !re.MatchString(pth) {
				skip++
				return nil
			}
		}

		files++
		switch {
		case re != nil:
			if !re.MatchString(pth) {
				skip++
				return nil
			}
		default:
			if _, ok := blacklist[filepath.Base(pth)]; ok {
				skip++
				return nil
			}
		}

		if *oTrace {
			fmt.Fprintln(os.Stderr, pth)
		}

		sources := []Source{
			{Name: "<predefined>", Value: cfg.Predefined},
			{Name: "<builtin>", Value: Builtin},
			{Name: pth},
		}
		_, err = Translate(cfg, sources)
		switch {
		case strings.Contains(pth, ".fail."):
			if err == nil {
				fails = append(fails, pth)
				t.Errorf("%v: missing error", pth)
			} else {
				if *oTrace {
					t.Log(err)
				}
				ok++
			}
		case err == nil:
			ok++
		default:
			cmd := exec.Command(cfg.CC, "-c", "-o", filepath.Join(tmp, "test.o"), pth)
			var buf bytes.Buffer
			cmd.Stderr = &buf
			if err2 := cmd.Run(); err2 != nil {
				t.Logf("%v: skip: %v: %s %v", pth, cfg.CC, buf.Bytes(), err2)
				skip++
				break
			}

			fails = append(fails, pth)
			t.Errorf("%v: %v", pth, err)
		}
		return nil
	})
	if err != nil {
		t.Error(err)
	}

	for _, v := range fails {
		t.Log(v)
	}
	t.Logf("files %v, skip %v, ok %v, fails %v", files, skip, ok, len(fails))
}

func TestTranslate(t *testing.T) {
	cfg := defaultCfg()
	if target == "darwin/arm64" {
		cfg.IgnoreStaticAssert = true
	}
	cfg.SysIncludePaths = append(cfg.SysIncludePaths, "Include") // benchmarksgame
	cfg.FS = cfs
	blacklistCompCert := map[string]struct{}{}
	blacklistMakarov := map[string]struct{}{}
	blacklistGCC := map[string]struct{}{
		"950919-1.c": {}, // Assertions are deprecated, not supported.
		"991014-1.c": {}, // Type size too big
	}
	blacklistDebian := map[string]struct{}{
		"nbody-4.c": {}, // _Float16 not supported
		"nbody-8.c": {}, // _Float16 not supported
	}
	switch fmt.Sprintf("%s/%s", runtime.GOOS, runtime.GOARCH) {
	case "linux/mips64le":
		blacklistCompCert["aes.c"] = struct{}{} // Unsupported endianness.
	case "linux/s390x":
		blacklistCompCert["aes.c"] = struct{}{} // Unsupported endianness.
	case "linux/loong64":
		blacklistCompCert["aes.c"] = struct{}{} // Unsupported endianness.
		blacklistGCC["20101011-1.c"] = struct{}{}
		blacklistGCC["fp-cmp-1.c"] = struct{}{}
		blacklistGCC["fp-cmp-2.c"] = struct{}{}
		blacklistGCC["fp-cmp-3.c"] = struct{}{}
	case "windows/arm64":
		blacklistGCC["pr49218.c"] = struct{}{}     //TODO
		blacklistGCC["pr54471.c"] = struct{}{}     //TODO
		blacklistGCC["pr61375.c"] = struct{}{}     //TODO
		blacklistGCC["pr63302.c"] = struct{}{}     //TODO
		blacklistGCC["pr65170.c"] = struct{}{}     //TODO
		blacklistGCC["pr70355.c"] = struct{}{}     //TODO
		blacklistGCC["pr84169.c"] = struct{}{}     //TODO
		blacklistGCC["pr84748.c"] = struct{}{}     //TODO
		blacklistGCC["pr85582-2.c"] = struct{}{}   //TODO
		blacklistGCC["pr85582-3.c"] = struct{}{}   //TODO
		blacklistGCC["pr92904.c"] = struct{}{}     //TODO
		blacklistGCC["pr93213.c"] = struct{}{}     //TODO
		blacklistGCC["pr98474.c"] = struct{}{}     //TODO
		blacklistMakarov["setjmp2.c"] = struct{}{} //TODO
	case "windows/amd64":
		blacklistGCC["pr30704.c"] = struct{}{} //TODO
	case "windows/386":
		blacklistGCC["pr30704.c"] = struct{}{} // '__int128' is not supported on this target
	case "freebsd/amd64":
		blacklistDebian["mandelbrot-6.c"] = struct{}{}
		blacklistDebian["spectral-norm-5.c"] = struct{}{}
	case "darwin/amd64":
		blacklistDebian["fannkuchredux-4.c"] = struct{}{}
		blacklistDebian["mandelbrot-6.c"] = struct{}{}
	}
	var files, ok, skip, fails int32
	for _, v := range []struct {
		cfg       *Config
		dir       string
		blacklist map[string]struct{}
	}{
		{cfg, "CompCert-3.6/test/c", blacklistCompCert},
		{cfg, "ccgo", nil},
		{cfg, "gcc-9.1.0/gcc/testsuite/gcc.c-torture", blacklistGCC},
		{cfg, "github.com/AbsInt/CompCert/test/c", blacklistCompCert},
		{cfg, "github.com/cxgo", nil},
		{cfg, "github.com/gcc-mirror/gcc/gcc/testsuite", blacklistGCC},
		{cfg, "github.com/vnmakarov", blacklistMakarov},
		{cfg, "sqlite-amalgamation", nil},
		{cfg, "tcc-0.9.27/tests/tests2", nil},
		{cfg, "benchmarksgame-team.pages.debian.net", blacklistDebian},
	} {
		if target == "windows/amd64" && strings.Contains(v.dir, "sqlite") {
			continue
		}

		t.Run(v.dir, func(t *testing.T) {
			f, o, s, n := testTranslate(t, v.cfg, "assets/"+v.dir, v.blacklist)
			files += f
			ok += o
			skip += s
			fails += n
		})
	}
	t.Logf("TOTAL: files %v, skip %v, ok %v, fails %v", files, skip, ok, fails)
}

func testTranslate(t *testing.T, cfg *Config, dir string, blacklist map[string]struct{}) (files, ok, skip, nfails int32) {
	tmp := t.TempDir()
	var fails []string
	p := newParallel()
	err := walk(dir, func(pth string, fi os.FileInfo) error {
		if fi.IsDir() {
			return nil
		}

		if filepath.Ext(pth) != ".c" {
			return nil
		}

		files++
		switch {
		case re != nil:
			if !re.MatchString(pth) {
				atomic.AddInt32(&skip, 1)
				return nil
			}
		default:
			if _, ok := blacklist[filepath.Base(pth)]; ok {
				atomic.AddInt32(&skip, 1)
				return nil
			}
		}
		apth := pth
		afi := fi
		p.exec(func() {
			if *oTrace {
				fmt.Fprintln(os.Stderr, apth)
			}

			var err error

			defer func() {
				p.Lock()

				defer p.Unlock()

				if err != nil {
					fails = append(fails, apth)
					t.Errorf("%v:\n%v", apth, err)
				}

			}()

			func() {
				defer func() {
					if e := recover(); e != nil && err == nil {
						err = fmt.Errorf("%v: PANIC: %v", apth, e)
						trc("%v: PANIC: %v\n%s", apth, e, debug.Stack())
						os.Exit(1)
					}
				}()

				if _, err = Translate(
					cfg,
					[]Source{
						{Name: "<predefined>", Value: cfg.Predefined},
						{Name: "<builtin>", Value: Builtin},
						{Name: apth, FS: cfs},
					},
				); err == nil {
					atomic.AddInt32(&ok, 1)
					return
				}
			}()

			if err == nil {
				return
			}

			f, err2 := cfs.Open(apth)
			if err2 != nil {
				err = errorf("", err2)
				return
			}

			defer f.Close()

			b := make([]byte, afi.Size())
			if n, _ := f.Read(b); int64(n) != afi.Size() {
				err = errorf("%v: short read", apth)
				return
			}

			fn := filepath.Join(tmp, filepath.Base(apth))
			if err2 := os.WriteFile(fn, b, 0660); err2 != nil {
				err = errorf("", err2)
				return
			}

			defer os.Remove(fn)

			cmd := exec.Command(cfg.CC, "-c", "-o", filepath.Join(tmp, "test.o"), fn)
			var buf bytes.Buffer
			cmd.Stderr = &buf
			if err2 = cmd.Run(); err2 != nil {
				t.Logf("%v: skip: %v: %s %v", apth, cfg.CC, buf.Bytes(), err2)
				atomic.AddInt32(&skip, 1)
				err = nil
				return
			}
		})
		return nil
	})
	if err != nil {
		t.Error(err)
	}

	p.wg.Wait()
	for _, v := range fails {
		t.Logf("FAIL: %v", v)
	}
	// fmt.Fprintf(os.Stderr, "%v: files %v, skip %v, ok %v, fails %v\n", dir, files, skip, ok, len(fails))
	t.Logf("files %v, skip %v, ok %v, fails %v", files, skip, ok, len(fails))
	return files, ok, skip, int32(len(fails))
}

func shell(cmd string, args ...string) ([]byte, error) {
	wd, err := absCwd()
	if err != nil {
		return nil, err
	}

	fmt.Printf("execute %s %q in %s\n", cmd, args, wd)
	var b echoWriter
	b.echo = testing.Verbose()
	c := exec.Command(cmd, args...)
	c.Stdout = &b
	c.Stderr = &b
	err = c.Run()
	return b.w.Bytes(), err
}

func absCwd() (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}

	if wd, err = filepath.Abs(wd); err != nil {
		return "", err
	}

	return wd, nil
}

type echoWriter struct {
	w    bytes.Buffer
	echo bool
}

func (w *echoWriter) Write(b []byte) (int, error) {
	if w.echo {
		os.Stdout.Write(b)
	}
	return w.w.Write(b)
}

func mustShell(t *testing.T, cmd string, args ...string) []byte {
	b, err := shell(cmd, args...)
	if err != nil {
		t.Fatalf("%v %s\noutput: %s\nerr: %s", cmd, args, b, err)
	}

	return b
}

func mustUntarFile(t *testing.T, dst, src string, canOverwrite func(fn string, fi os.FileInfo) bool) {
	if err := untarFile(dst, src, canOverwrite); err != nil {
		t.Fatal(err)
	}
}

func untarFile(dst, src string, canOverwrite func(fn string, fi os.FileInfo) bool) error {
	f, err := cfs.Open(src)
	if err != nil {
		return err
	}

	defer f.Close()

	return untar(dst, bufio.NewReader(f), canOverwrite)
}

func untar(dst string, r io.Reader, canOverwrite func(fn string, fi os.FileInfo) bool) error {
	dst = filepath.FromSlash(dst)
	gr, err := gzip.NewReader(r)
	if err != nil {
		return err
	}

	tr := tar.NewReader(gr)
	for {
		hdr, err := tr.Next()
		if err != nil {
			if err != io.EOF {
				return err
			}

			return nil
		}

		switch hdr.Typeflag {
		case tar.TypeDir:
			dir := filepath.Join(dst, hdr.Name)
			if err = os.MkdirAll(dir, 0770); err != nil {
				return err
			}
		case tar.TypeSymlink, tar.TypeXGlobalHeader:
			// skip
		case tar.TypeReg:
			dir := filepath.Dir(filepath.Join(dst, hdr.Name))
			if _, err := os.Stat(dir); err != nil {
				if !os.IsNotExist(err) {
					return err
				}

				if err = os.MkdirAll(dir, 0770); err != nil {
					return err
				}
			}

			fn := filepath.Join(dst, hdr.Name)
			f, err := os.OpenFile(fn, os.O_CREATE|os.O_WRONLY, os.FileMode(hdr.Mode))
			if err != nil {
				return err
			}

			w := bufio.NewWriter(f)
			if _, err = io.Copy(w, tr); err != nil {
				return err
			}

			if err := w.Flush(); err != nil {
				return err
			}

			if err := f.Close(); err != nil {
				return err
			}

			if err := os.Chtimes(fn, hdr.AccessTime, hdr.ModTime); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unexpected tar header typeflag %#02x", hdr.Typeflag)
		}
	}

}

func TestMake(t *testing.T) {
	if testing.Short() {
		t.Skip("SKIP: -short")
	}

	if goos == "windows" {
		t.Skip("SKIP: not supported on Windows")
	}

	tmp := t.TempDir()
	CC := defaultCfg().CC
	if !filepath.IsAbs(CC) {
		var err error
		if CC, err = filepath.Abs(CC); err != nil {
			t.Fatal(err)
		}
	}
	os.Setenv("FAKE_CC_CC", CC)
	base := filepath.Base(CC)
	oldCC := os.Getenv("CC")

	defer os.Setenv("CC", oldCC)

	os.Setenv("CC", base)
	cc := filepath.Join(tmp, base)
	args := []string{"build", "-o", cc}
	if Dmesgs {
		args = append(args, "-tags=cc.dmesg")
	}
	mustShell(t, "go", append(args, "fakecc.go")...)
	if !strings.HasPrefix(base, "gcc") {
		mustShell(t, "cp", cc, filepath.Join(tmp, "gcc"))
	}
	if !strings.HasPrefix(base, "cc") {
		mustShell(t, "cp", cc, filepath.Join(tmp, "cc"))
	}
	if !strings.HasPrefix(base, "clang") {
		mustShell(t, "cp", cc, filepath.Join(tmp, "clang"))
	}
	oldPath := os.Getenv("PATH")

	defer os.Setenv("PATH", oldPath)

	os.Setenv("PATH", tmp+string(os.PathListSeparator)+oldPath)
	var files, ok, skip, fails int32
	all := []string{
		"darwin/amd64",
		"darwin/arm64",
		"freebsd/386",
		"freebsd/amd64",
		"linux/386",
		"linux/amd64",
		"linux/arm",
		"linux/arm64",
		"linux/loong64",
		"linux/riscv64",
		"linux/s390x",
		"netbsd/amd64",
		"openbsd/amd64",
	}
	hdf5 := []string{
		"freebsd/386",
		"freebsd/amd64",
		"linux/386",
		"linux/amd64",
		"linux/arm",
		"linux/arm64",
		"linux/loong64",
		"linux/riscv64",
		"linux/s390x",
		"netbsd/amd64",
		"openbsd/amd64",
		// "darwin/amd64", //TODO PATH_MAX undefined
		// "darwin/arm64", //TODO PATH_MAX undefined
	}
	mpfr := []string{
		"darwin/amd64",
		"darwin/arm64",
		"freebsd/386",
		"freebsd/amd64",
		"linux/386",
		"linux/amd64",
		"linux/arm",
		"linux/arm64",
		"linux/loong64",
		"linux/s390x",
		"netbsd/amd64",
		"openbsd/amd64",
		// "linux/riscv64", //TODO
	}
	qbe := []string{
		"amd64",
		"arm64",
		"loong64",
		"riscv64",
	}
	qjs := []string{
		"freebsd/386",
		"freebsd/amd64",
		"linux/386",
		"linux/amd64",
		"linux/arm",
		"linux/arm64",
		"linux/loong64",
		"linux/riscv64",
		"linux/s390x",
		"netbsd/amd64",
		"openbsd/amd64",
		// "darwin/amd64", //TODO PATH_MAX undefined
		// "darwin/arm64", //TODO PATH_MAX undefined
	}
	redis := []string{
		"freebsd/386",
		"freebsd/amd64",
		"linux/386",
		"linux/amd64",
		"linux/arm64",
		"linux/loong64",
		"linux/riscv64",
		"linux/s390x",
		"openbsd/amd64",
		// "darwin/amd64", //TODO SSIZE_MAX undefined
		// "darwin/arm64", //TODO SSIZE_MAX undefined
		// "linux/arm",    //TODO <sys/event.h> not found
		// "netbsd/amd64", //TODO <sys/epoll.h> not found
	}
	cfg := &makeCfg{cc: cc}
	switch goos {
	case "darwin":
		cfg.cflags = "-I/opt/homebrew/include"
	case "freebsd", "openbsd;":
		cfg.cflags = "-I/usr/local/include"
	case "netbsd":
		cfg.cflags = "-I/usr/pkg/include"
	}
	cfg.cflags = strings.TrimSpace(cfg.cflags)
	for _, v := range []struct {
		archive string
		dir     string
		cfg     *makeCfg
		filter  []string
	}{
		{"ftp.pcre.org/pub/pcre.tar.gz", "pcre", cfg.add("--disable-cpp"), all},
		{"ftp.pcre.org/pub/pcre2.tar.gz", "pcre2", cfg, all},
		{"github.com/madler/zlib.tar.gz", "zlib", cfg, all},
		{"sourceforge.net/projects/tcl/files/Tcl/tcl.tar.gz", "tcl/unix", cfg.add("--enable-corefoundation=no"), all},
		{"gmplib.org/download/gmp/gmp-6.2.1.tar.gz", "gmp-6.2.1", cfg, all},
		{"www.mpfr.org/mpfr-current/mpfr-4.1.0.tar.gz", "mpfr-4.1.0", cfg, mpfr},
		{"ftp.gnu.org/gnu/mpc/mpc-1.2.1.tar.gz", "mpc-1.2.1", cfg, all},
		{"www.hdfgroup.org/downloads/hdf5/source-code/hdf5-1.12.1.tar.gz", "hdf5-1.12.1", cfg, hdf5},
		{"musl.libc.org/releases/musl-1.2.4.tar.gz", "musl-1.2.4", cfg, []string{"linux"}},
		{"github.com/git/git/archive/refs/tags/v2.35.1.tar.gz", "git-2.35.1", cfg.noConfigure(), all},
		{"github.com/bellard/quickjs/archive/refs/heads/quickjs-master/quickjs-master.tar.gz", "quickjs-master", cfg.noConfigure(), qjs},
		{"download.redis.io/releases/redis-6.2.6.tar.gz", "redis-6.2.6", cfg.noConfigure(), redis},
		{"c9x.me/git/qbe.tar.gz", "qbe", cfg.noConfigure(), qbe},
		{"git.postgresql.org/git/postgresql.tar.gz", "postgresql", cfg, all},
		{"www.lua.org/ftp/lua-5.4.4.tar.gz", "lua-5.4.4", cfg.noConfigure(), all},
	} {
		if !filter(v.filter) || re != nil && !re.MatchString(v.archive) {
			continue
		}

		t.Run(v.dir, func(t *testing.T) {
			f, o, s, n := testMake(t, "assets/"+v.archive, v.dir, v.cfg)
			files += f
			ok += o
			skip += s
			fails += n
		})
	}
	t.Logf("TOTAL: files %v, skip %v, ok %v, fails %v", files, skip, ok, fails)
}

func filter(f []string) bool {
	for _, v := range f {
		if v == goos || v == goarch || v == target {
			return true
		}
	}

	return false
}

type makeCfg struct {
	cc        string
	cflags    string
	configure []string

	disableConfigure bool
}

func (n *makeCfg) add(a ...string) *makeCfg {
	m := *n
	m.configure = append(m.configure[:len(m.configure):len(m.configure)], a...)
	return &m
}

func (n *makeCfg) noConfigure() *makeCfg {
	m := *n
	m.disableConfigure = true
	return &m
}

func testMake(t *testing.T, archive, dir string, mcfg *makeCfg) (files, ok, skip, nfails int32) {
	tmp := t.TempDir()
	mustUntarFile(t, tmp, archive, nil)
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}

	newWd := filepath.Join(tmp, dir)
	if err := os.Chdir(newWd); err != nil {
		t.Fatal(err)
	}

	defer func() {
		if err := os.Chdir(wd); err != nil {
			t.Fatal(err)
		}
	}()

	fn := filepath.Join(newWd, "_fake_cc.log")
	os.Setenv("FAKE_CC_LOG", fn)
	var args []string
	if mcfg != nil {
		if mcfg.cflags != "" {
			os.Setenv("CFLAGS", mcfg.cflags)
		}
		args = mcfg.configure
	}
	if !mcfg.disableConfigure {
		if _, err := shell("./configure", args...); err != nil {
			t.Skipf("SKIP: ./configure: %v", err)
		}
	}
	switch goos {
	case "darwin", "freebsd", "netbsd", "openbsd":
		if _, err := shell("gmake", makeJ); err != nil {
			t.Skipf("SKIP: gmake: %v", err)
		}
	default:
		if _, err := shell("make", makeJ); err != nil {
			t.Skipf("SKIP: make: %v", err)
		}
	}
	logf, err := os.ReadFile(fn)
	if err != nil {
		t.Fatal(err)
	}

	lines := bytes.Split(logf, []byte{'\n'})
	for _, b := range lines {
		if b = bytes.TrimSpace(b); len(b) == 0 {
			continue
		}

		var a []string
		if err := json.NewDecoder(bytes.NewReader(b)).Decode(&a); err != nil {
			t.Fatal(err)
		}

		if len(a) == 0 {
			t.Fatal("unexpected empty line")
		}

		if *oTrace {
			fmt.Printf("%s\n", strings.Join(a, " "))
		}
		files++
		switch a[0] {
		case "FAIL":
			t.Errorf("%s", strings.Join(a, " "))
			nfails++
		case "PASS":
			ok++
		case "SKIP":
			skip++
		default:
			t.Fatalf("unexpected report tag %q", a[0])
		}
	}
	t.Logf("files %v, skip %v, ok %v, fails %v", files, skip, ok, nfails)
	return files, ok, skip, nfails
}

func TestSep(t *testing.T) {
	for i, v := range []struct {
		src         string
		sep         string
		trailingSep string
	}{
		{"int f() {}", "", "\n"},
		{" int f() {}\n", " ", "\n"},
		{"\nint f() {}\n", "\n", "\n"},
		{"/*A*//*B*/int f() {}\n", "/*A*//*B*/", "\n"},
		{"/*A*//*B*/ int f() {}\n", "/*A*//*B*/ ", "\n"},

		{"/*A*//*B*/\nint f() {}\n", "/*A*//*B*/\n", "\n"},
		{"/*A*/ /*B*/int f() {}\n", "/*A*/ /*B*/", "\n"},
		{"/*A*/ /*B*/ int f() {}\n", "/*A*/ /*B*/ ", "\n"},
		{"/*A*/ /*B*/\nint f() {}\n", "/*A*/ /*B*/\n", "\n"},
		{"/*A*/\n/*B*/int f() {}\n", "/*A*/\n/*B*/", "\n"},

		{"/*A*/\n/*B*/ int f() {}\n", "/*A*/\n/*B*/ ", "\n"},
		{"/*A*/\n/*B*/\nint f() {}\n", "/*A*/\n/*B*/\n", "\n"},
		{" /*A*/ /*B*/int f() {}\n", " /*A*/ /*B*/", "\n"},
		{" /*A*/ /*B*/ int f() {}\n", " /*A*/ /*B*/ ", "\n"},
		{" /*A*/ /*B*/\nint f() {}\n", " /*A*/ /*B*/\n", "\n"},

		{" /*A*/\n/*B*/int f() {}\n", " /*A*/\n/*B*/", "\n"},
		{" /*A*/\n/*B*/ int f() {}\n", " /*A*/\n/*B*/ ", "\n"},
		{" /*A*/\n/*B*/\nint f() {}\n", " /*A*/\n/*B*/\n", "\n"},
		{"\n/*A*/ /*B*/int f() {}\n", "\n/*A*/ /*B*/", "\n"},
		{"\n/*A*/ /*B*/ int f() {}\n", "\n/*A*/ /*B*/ ", "\n"},

		{"\n/*A*/ /*B*/\nint f() {}\n", "\n/*A*/ /*B*/\n", "\n"},
		{"\n/*A*/\n/*B*/int f() {}\n", "\n/*A*/\n/*B*/", "\n"},
		{"\n/*A*/\n/*B*/ int f() {}\n", "\n/*A*/\n/*B*/ ", "\n"},
		{"\n/*A*/\n/*B*/\nint f() {}\n", "\n/*A*/\n/*B*/\n", "\n"},
	} {
		ast, err := Parse(
			&Config{},
			[]Source{{Name: "test", Value: v.src + "int __predefined_declarator;"}},
		)
		if err != nil {
			t.Errorf("%v: %v", i, err)
			continue
		}

		t.Logf("%q -> %q", v.src, NodeSource(ast.TranslationUnit))
		var tok Token
		firstToken(ast.TranslationUnit, &tok)
		if g, e := string(tok.Sep()), v.sep; g != e {
			t.Errorf("%v: %q %q", i, g, e)
		}
		if g, e := string(ast.EOF.Sep()), v.trailingSep; g != e {
			t.Errorf("%v: %q %q", i, g, e)
		}
	}
}

func firstToken(n Node, r *Token) {
	if n == nil {
		return
	}

	if x, ok := n.(*LabeledStatement); ok && x != nil {
		t := x.Token
		if r.Seq() == 0 || t.Seq() < r.Seq() {
			*r = t
		}
		return
	}

	if x, ok := n.(Token); ok && x.Seq() != 0 {
		if r.Seq() == 0 || x.Seq() < r.Seq() {
			*r = x
		}
		return
	}

	t := reflect.TypeOf(n)
	v := reflect.ValueOf(n)
	var zero reflect.Value
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
		v = v.Elem()
	}
	if v == zero || v.IsZero() || t.Kind() != reflect.Struct {
		return
	}

	nf := t.NumField()
	for i := 0; i < nf; i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}

		if m, ok := v.Field(i).Interface().(Node); ok {
			firstToken(m, r)
		}
	}
}

// https://gitlab.com/cznic/cc/-/issues/141
func TestIssue141(t *testing.T) {
	cfg := defaultCfg()
	ast, err := Translate(cfg, []Source{
		{Name: "<predefined>", Value: cfg.Predefined},
		{Name: "<builtin>", Value: Builtin},
		{Name: "141.c", Value: `
typedef struct {
        int i;
        char s[];
} t;

t x = {1, "a"};
t y = {2, "bc"};
`},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, nm := range []string{"t", "x", "y"} {
		s := ast.Scope.Nodes[nm]
		if len(s) == 0 {
			t.Fatal(nm)
		}

		x, ok := s[0].(*Declarator)
		if !ok {
			t.Fatal(nm)
		}

		structType, ok := x.Type().(*StructType)
		if !ok {
			t.Fatal(nm)
		}

		if g, e := structType.IsIncomplete(), false; g != e {
			t.Errorf("%s: declarator: %s, IsIncomplete got %v, exp %v", nm, structType, g, e)
		}

		if g, e := structType.Size(), ast.Int.Size(); g != e {
			t.Errorf("%s: declarator: %s, size got %v, exp %v", nm, structType, g, e)
		}

		if g, e := structType.Padding(), 0; g != e {
			t.Errorf("%s: declarator: %s, padding got %v, exp %v", nm, structType, g, e)
		}

		field := structType.FieldByName("s")
		if field == nil {
			t.Fatal(nm)
		}

		fieldType := field.Type()
		if g, e := fieldType.IsIncomplete(), true; g != e {
			t.Errorf("%s.s: declarator: %s, IsIncomplete got %v, exp %v", nm, fieldType, g, e)
		}

		if g, e := fieldType.Size(), int64(-1); g != e {
			t.Errorf("%s.s: declarator: %s, size got %v, exp %v", nm, fieldType, g, e)
		}

	}
	for l := ast.TranslationUnit; l != nil; l = l.TranslationUnit {
		ed := l.ExternalDeclaration
		if ed.Case != ExternalDeclarationDecl {
			continue
		}

		d := ed.Declaration
		if d == nil {
			continue
		}

		idl := d.InitDeclaratorList
		if idl == nil {
			continue
		}

		id := idl.InitDeclarator
		sz2 := 3
		switch nm := id.Declarator.Name(); nm {
		case "x":
			sz2 = 2
			fallthrough
		case "y":
			structType, ok := id.Initializer.Type().(*StructType)
			if !ok {
				t.Fatal(nm)
			}

			if g, e := structType.IsIncomplete(), false; g != e {
				t.Errorf("%s: initializer: %s, IsIncomplete got %v, exp %v", nm, structType, g, e)
			}

			if g, e := structType.Size(), 2*ast.Int.Size(); g != e {
				t.Errorf("%s: initializer: %s, size got %v, exp %v", nm, structType, g, e)
			}

			if g, e := structType.Padding(), int(ast.Int.Size())-sz2; g != e {
				t.Errorf("%s: initializer: %s, padding got %v, exp %v", nm, structType, g, e)
			}

			field := structType.FieldByName("s")
			if field == nil {
				t.Fatal(nm)
			}

			fieldType := field.Type()
			if g, e := fieldType.IsIncomplete(), false; g != e {
				t.Errorf("%s.s: initializer: %s, IsIncomplete got %v, exp %v", nm, fieldType, g, e)
			}

			if g, e := fieldType.Size(), int64(sz2); g != e {
				t.Errorf("%s.s: initializer: %s, size got %v, exp %v", nm, fieldType, g, e)
			}
		}
	}
}

func TestABI(t *testing.T) {
	for _, osarch := range []string{
		"darwin/amd64",
		"darwin/arm64",
		"freebsd/386",
		"freebsd/amd64",
		"freebsd/arm",
		"freebsd/arm64",
		"linux/386",
		"linux/amd64",
		"linux/arm",
		"linux/arm64",
		"linux/ppc64le",
		"linux/loong64",
		"linux/riscv64",
		"linux/s390x",
		"netbsd/amd64",
		"netbsd/arm",
		"openbsd/amd64",
		"openbsd/arm64",
		"windows/386",
		"windows/amd64",
		"windows/arm64",
	} {
		a := strings.Split(osarch, "/")
		os := a[0]
		arch := a[1]
		if _, err := NewABI(os, arch); err != nil {
			t.Errorf("%s: %v", osarch, err)
		}
	}
}

// TestBitfieldByteOrder pins the storage layout of bit fields, which is
// endianness dependent: little-endian ABIs allocate a storage unit's bit fields
// starting from the least significant bit, big-endian ABIs from the most
// significant bit. The regression case is GMP's 'union ieee_double_extract',
// which overlays a bit field struct on a double; getting the big-endian
// (s390x) layout wrong silently produced garbage doubles in mpz/mpq/mpf
// _get_d/_set_d. Each target below declares the union the way GMP does for that
// endianness, so both must yield the same physical IEEE-754 double layout:
// sig at bit 63, exp at bits 52..62, mantissa in bits 0..51.
func TestBitfieldByteOrder(t *testing.T) {
	type field struct {
		name       string
		offset     int64 // Field.Offset(), bytes from the start of the storage
		offsetBits int   // Field.OffsetBits(), bit position within the access unit
		mask       uint64
	}
	for _, tc := range []struct {
		arch   string
		src    string
		fields []field
	}{
		{ // big-endian: GMP declares the fields most-significant first
			arch: "s390x",
			src: `union ieee_double_extract {
	struct { unsigned sig:1; unsigned exp:11; unsigned manh:20; unsigned manl:32; } s;
	double d;
} v;`,
			fields: []field{
				{"sig", 0, 31, 0x80000000},
				{"exp", 0, 20, 0x7ff00000},
				{"manh", 0, 0, 0x000fffff},
				{"manl", 4, 0, 0xffffffff},
			},
		},
		{ // little-endian: GMP declares the fields least-significant first
			arch: "amd64",
			src: `union ieee_double_extract {
	struct { unsigned manl:32; unsigned manh:20; unsigned exp:11; unsigned sig:1; } s;
	double d;
} v;`,
			fields: []field{
				{"manl", 0, 0, 0xffffffff},
				{"manh", 4, 0, 0x000fffff},
				{"exp", 4, 20, 0x7ff00000},
				{"sig", 4, 31, 0x80000000},
			},
		},
	} {
		cfg, err := NewConfig("linux", tc.arch, LongDouble64Flag("linux", tc.arch))
		if err != nil {
			t.Fatalf("%s: NewConfig: %v", tc.arch, err)
		}

		ast, err := Translate(cfg, []Source{
			{Name: "<predefined>", Value: cfg.Predefined},
			{Name: "<builtin>", Value: Builtin},
			{Name: "test.c", Value: tc.src},
		})
		if err != nil {
			t.Fatalf("%s: %v", tc.arch, err)
		}

		s := ast.Scope.Nodes["v"]
		if len(s) == 0 {
			t.Fatalf("%s: v not declared", tc.arch)
		}
		d, ok := s[0].(*Declarator)
		if !ok {
			t.Fatalf("%s: v is %T, want *Declarator", tc.arch, s[0])
		}
		u, ok := d.Type().(*UnionType)
		if !ok {
			t.Fatalf("%s: v has type %T, want *UnionType", tc.arch, d.Type())
		}
		sf := u.FieldByName("s")
		if sf == nil {
			t.Fatalf("%s: union field s not found", tc.arch)
		}
		st, ok := sf.Type().(*StructType)
		if !ok {
			t.Fatalf("%s: s has type %T, want *StructType", tc.arch, sf.Type())
		}

		for _, w := range tc.fields {
			f := st.FieldByName(w.name)
			if f == nil {
				t.Errorf("%s: field %s not found", tc.arch, w.name)
				continue
			}
			if g, e := f.Offset(), w.offset; g != e {
				t.Errorf("%s: %s.Offset() = %d, want %d", tc.arch, w.name, g, e)
			}
			if g, e := f.OffsetBits(), w.offsetBits; g != e {
				t.Errorf("%s: %s.OffsetBits() = %d, want %d", tc.arch, w.name, g, e)
			}
			if g, e := f.Mask(), w.mask; g != e {
				t.Errorf("%s: %s.Mask() = %#x, want %#x", tc.arch, w.name, g, e)
			}
		}
	}
}

// TestUnionBitfieldByteOrder is the union counterpart of TestBitfieldByteOrder:
// a bit field declared directly in a union starts at offset 0 and, on a
// big-endian target, is allocated from the most significant bit of its access
// unit (bits2AccessBytes(ValueBits) bytes) rather than the least significant
// one. Access-unit widths of 1, 2, 4 and 8 bytes are all exercised.
func TestUnionBitfieldByteOrder(t *testing.T) {
	const src = `union U {
	unsigned a:1;
	unsigned b:12;
	unsigned c:20;
	unsigned long long e:40;
} v;`
	type field struct {
		name       string
		offsetBits int
		mask       uint64
	}
	for _, tc := range []struct {
		arch   string
		fields []field
	}{
		{"s390x", []field{ // big-endian: fields sit at the top of their access unit
			{"a", 7, 0x80},
			{"b", 4, 0xfff0},
			{"c", 12, 0xfffff000},
			{"e", 24, 0xffffffffff000000},
		}},
		{"amd64", []field{ // little-endian: fields sit at the bottom
			{"a", 0, 0x1},
			{"b", 0, 0xfff},
			{"c", 0, 0xfffff},
			{"e", 0, 0xffffffffff},
		}},
	} {
		cfg, err := NewConfig("linux", tc.arch, LongDouble64Flag("linux", tc.arch))
		if err != nil {
			t.Fatalf("%s: NewConfig: %v", tc.arch, err)
		}

		ast, err := Translate(cfg, []Source{
			{Name: "<predefined>", Value: cfg.Predefined},
			{Name: "<builtin>", Value: Builtin},
			{Name: "test.c", Value: src},
		})
		if err != nil {
			t.Fatalf("%s: %v", tc.arch, err)
		}

		s := ast.Scope.Nodes["v"]
		if len(s) == 0 {
			t.Fatalf("%s: v not declared", tc.arch)
		}
		d, ok := s[0].(*Declarator)
		if !ok {
			t.Fatalf("%s: v is %T, want *Declarator", tc.arch, s[0])
		}
		u, ok := d.Type().(*UnionType)
		if !ok {
			t.Fatalf("%s: v has type %T, want *UnionType", tc.arch, d.Type())
		}

		for _, w := range tc.fields {
			f := u.FieldByName(w.name)
			if f == nil {
				t.Errorf("%s: field %s not found", tc.arch, w.name)
				continue
			}
			if g, e := f.Offset(), int64(0); g != e {
				t.Errorf("%s: %s.Offset() = %d, want %d", tc.arch, w.name, g, e)
			}
			if g, e := f.OffsetBits(), w.offsetBits; g != e {
				t.Errorf("%s: %s.OffsetBits() = %d, want %d", tc.arch, w.name, g, e)
			}
			if g, e := f.Mask(), w.mask; g != e {
				t.Errorf("%s: %s.Mask() = %#x, want %#x", tc.arch, w.name, g, e)
			}
		}
	}
}

// TestFixArgs verifies that newConfig strips a flag the host compiler rejects,
// regardless of how the compiler phrases the rejection. The regression:
// TestBitfieldByteOrder cross-configures linux/s390x by passing -mlong-double-64
// to the host compiler; on arm64 hosts (darwin/arm64, windows/arm64,
// openbsd/arm64) clang rejects it as "unsupported option '...' for target
// '...'", a wording fixArgs did not recognize, so newConfig failed with "cannot
// determine C compiler configuration". Rather than enumerate every wording,
// fixArgs now strips any flag it passed that the diagnostic quotes.
func TestFixArgs(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		msg  string
		want []string
		ok   bool
	}{
		{
			name: "gcc unrecognized",
			args: []string{"-mlong-double-64", "-E"},
			msg:  "gcc: error: unrecognized command-line option '-mlong-double-64'",
			want: []string{"-E"},
			ok:   true,
		},
		{
			name: "clang unknown argument",
			args: []string{"-mlong-double-64", "-E"},
			msg:  "clang: error: unknown argument: '-mlong-double-64'",
			want: []string{"-E"},
			ok:   true,
		},
		{
			name: "clang unsupported for target (darwin/arm64)",
			args: []string{"-mlong-double-64", "-E"},
			msg:  "clang: error: unsupported option '-mlong-double-64' for target 'arm64-apple-macosx26.0.0'",
			want: []string{"-E"},
			ok:   true,
		},
		{
			name: "clang unsupported for target (openbsd/arm64)",
			args: []string{"-mlong-double-64", "-E"},
			msg:  "cc: error: unsupported option '-mlong-double-64' for target 'aarch64-unknown-openbsd7.3'",
			want: []string{"-E"},
			ok:   true,
		},
		{
			// Insurance for windows/arm64 and any future host: an unknown
			// wording still gets fixed as long as the flag is quoted.
			name: "novel wording, flag still quoted",
			args: []string{"-mlong-double-64", "-E"},
			msg:  "cc1: sorry, unimplemented: '-mlong-double-64' is not available for this target",
			want: []string{"-E"},
			ok:   true,
		},
		{
			// GCC quotes with U+2018/U+2019; the old straight-quote extraction
			// silently missed these.
			name: "gcc curly quotes",
			args: []string{"-mlong-double-64", "-E"},
			msg:  "gcc: error: unrecognized command-line option ‘-mlong-double-64’",
			want: []string{"-E"},
			ok:   true,
		},
		{
			name: "unrelated error keeps args",
			args: []string{"-mlong-double-64", "-E"},
			msg:  "cc: error: no input files",
			want: []string{"-mlong-double-64", "-E"},
			ok:   false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := append([]string(nil), tc.args...)
			if g, e := fixArgs(&args, tc.msg), tc.ok; g != e {
				t.Errorf("fixArgs returned %v, want %v", g, e)
			}
			if g, e := args, tc.want; !reflect.DeepEqual(g, e) {
				t.Errorf("args = %q, want %q", g, e)
			}
		})
	}
}

// https://gitlab.com/cznic/cc/-/issues/146
func TestIssue146(t *testing.T) {
	const src = `enum foo {
	/* First comment */
	FOO_A,
	/* Second comment */
	FOO_B,
};
`

	cfg, err := NewConfig(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		t.Fatalf("failed to create new config: %v", err)
	}

	sources := []Source{
		{Name: "<predefined>", Value: cfg.Predefined},
		{Name: "<builtin>", Value: Builtin},
		{Name: "test.h", Value: src},
	}

	ast, err := Parse(cfg, sources)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}

	var toks []Token
	for tu := ast.TranslationUnit; tu != nil; tu = tu.TranslationUnit {
		if tu.ExternalDeclaration == nil {
			continue
		}
		if tu.ExternalDeclaration.Case != ExternalDeclarationDecl {
			continue
		}
		decl := tu.ExternalDeclaration.Declaration

		if decl.Position().Filename != "test.h" {
			continue
		}

		enumSpec := decl.DeclarationSpecifiers.TypeSpecifier.EnumSpecifier
		for el := enumSpec.EnumeratorList; el != nil; el = el.EnumeratorList {
			t.Logf("el.Enumerator.Token: %v", el.Enumerator.Token)
			toks = append(toks, el.Enumerator.Token)
			t.Logf("el.Enumerator.Token.Sep(): %q", string(el.Enumerator.Token.Sep()))
			t.Logf("el.Token: %v", el.Token)
			t.Logf("el.Token.Sep(): %q", string(el.Token.Sep()))
			t.Log("---")
		}
	}
	for i, v := range []string{
		"\n\t/* First comment */\n\t",
		"\n\t/* Second comment */\n\t",
	} {
		if g, e := string(toks[i].Sep()), v; g != e {
			t.Errorf("%v: %q %q", i, g, e)
		}
	}
}

// https://gitlab.com/cznic/cc/-/issues/149
func TestIssue149(t *testing.T) {
	// https://gitlab.com/cznic/cc/-/issues/149#note_1376776631
	const src = "#include_next <sys/types.h>\n"
	if runtime.GOOS == "windows" {
		return
	}

	cfg, err := NewConfig(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		t.Fatalf("failed to create new config: %v", err)
	}

	sources := []Source{
		{Name: "<predefined>", Value: cfg.Predefined},
		{Name: "<builtin>", Value: Builtin},
		{Name: "test.h", Value: src},
	}

	if _, err := Parse(cfg, sources); err != nil {
		t.Fatalf("parse error: %v", err)
	}
}

func TestConst0(t *testing.T) {
	const src = "int i;\n"
	cfg, err := NewConfig(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		t.Fatalf("failed to create new config: %v", err)
	}

	sources := []Source{
		{Name: "<predefined>", Value: cfg.Predefined},
		{Name: "<builtin>", Value: Builtin},
		{Name: "test.h", Value: src},
	}

	ast, err := Translate(cfg, sources)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}

	d := ast.Scope.Nodes["i"][0].(*Declarator)
	if g, e := d.IsConst(), false; g != e {
		t.Error(g, e)
	}

	if g, e := d.Type().Attributes().IsConst(), false; g != e {
		t.Error(g, e)
	}
}

func TestConst1(t *testing.T) {
	const src = "const int i;\n"
	cfg, err := NewConfig(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		t.Fatalf("failed to create new config: %v", err)
	}

	sources := []Source{
		{Name: "<predefined>", Value: cfg.Predefined},
		{Name: "<builtin>", Value: Builtin},
		{Name: "test.h", Value: src},
	}

	ast, err := Translate(cfg, sources)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}

	d := ast.Scope.Nodes["i"][0].(*Declarator)
	t.Log(d.Type())
	if g, e := d.IsConst(), true; g != e {
		t.Error(g, e)
	}

	if g, e := d.Type().Attributes().IsConst(), true; g != e {
		t.Error(g, e)
	}
}

func TestConst2(t *testing.T) {
	const src = "const int *p;\n"
	cfg, err := NewConfig(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		t.Fatalf("failed to create new config: %v", err)
	}

	sources := []Source{
		{Name: "<predefined>", Value: cfg.Predefined},
		{Name: "<builtin>", Value: Builtin},
		{Name: "test.h", Value: src},
	}

	ast, err := Translate(cfg, sources)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}

	d := ast.Scope.Nodes["p"][0].(*Declarator)
	t.Log(d.Type())
	if g, e := d.IsConst(), false; g != e {
		t.Error(g, e)
	}

	if g, e := d.Type().Attributes().IsConst(), false; g != e {
		t.Error(g, e)
	}

	if g, e := d.Type().(*PointerType).Elem().Attributes().IsConst(), true; g != e {
		t.Error(g, e)
	}
}

func TestConst3(t *testing.T) {
	const src = "int * const p;\n"
	cfg, err := NewConfig(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		t.Fatalf("failed to create new config: %v", err)
	}

	sources := []Source{
		{Name: "<predefined>", Value: cfg.Predefined},
		{Name: "<builtin>", Value: Builtin},
		{Name: "test.h", Value: src},
	}

	ast, err := Translate(cfg, sources)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}

	d := ast.Scope.Nodes["p"][0].(*Declarator)
	t.Log(d.Type())
	if g, e := d.IsConst(), true; g != e {
		t.Error(g, e) //TODO
	}

	if g, e := d.Type().Attributes().IsConst(), true; g != e {
		t.Error(g, e) //TODO
	}

	if g, e := d.Type().(*PointerType).Elem().Attributes().IsConst(), false; g != e {
		t.Error(g, e)
	}
}

func TestConst4(t *testing.T) {
	const src = "const int * const p;\n"
	cfg, err := NewConfig(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		t.Fatalf("failed to create new config: %v", err)
	}

	sources := []Source{
		{Name: "<predefined>", Value: cfg.Predefined},
		{Name: "<builtin>", Value: Builtin},
		{Name: "test.h", Value: src},
	}

	ast, err := Translate(cfg, sources)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}

	d := ast.Scope.Nodes["p"][0].(*Declarator)
	t.Log(d.Type())
	if g, e := d.IsConst(), true; g != e {
		t.Error(g, e)
	}

	if g, e := d.Type().Attributes().IsConst(), true; g != e {
		t.Error(g, e)
	}

	if g, e := d.Type().(*PointerType).Elem().Attributes().IsConst(), true; g != e {
		t.Error(g, e)
	}
}

func TestConst5(t *testing.T) {
	const src = "const int *p[];\n"
	cfg, err := NewConfig(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		t.Fatalf("failed to create new config: %v", err)
	}

	sources := []Source{
		{Name: "<predefined>", Value: cfg.Predefined},
		{Name: "<builtin>", Value: Builtin},
		{Name: "test.h", Value: src},
	}

	ast, err := Translate(cfg, sources)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}

	d := ast.Scope.Nodes["p"][0].(*Declarator)
	t.Log(d.Type())
	if g, e := d.IsConst(), false; g != e {
		t.Error(g, e)
	}

	if g, e := d.Type().Attributes().IsConst(), false; g != e {
		t.Error(g, e)
	}

	if g, e := d.Type().(*ArrayType).Elem().Attributes().IsConst(), true; g != e {
		t.Error(g, e)
	}
}

func TestVolatile0(t *testing.T) {
	const src = "int i;\n"
	cfg, err := NewConfig(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		t.Fatalf("failed to create new config: %v", err)
	}

	sources := []Source{
		{Name: "<predefined>", Value: cfg.Predefined},
		{Name: "<builtin>", Value: Builtin},
		{Name: "test.h", Value: src},
	}

	ast, err := Translate(cfg, sources)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}

	d := ast.Scope.Nodes["i"][0].(*Declarator)
	t.Log(d.Type())
	if g, e := d.IsVolatile(), false; g != e {
		t.Error(g, e)
	}

	if g, e := d.Type().Attributes().IsVolatile(), false; g != e {
		t.Error(g, e)
	}
}

func TestVolatile1(t *testing.T) {
	const src = "volatile int i;\n"
	cfg, err := NewConfig(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		t.Fatalf("failed to create new config: %v", err)
	}

	sources := []Source{
		{Name: "<predefined>", Value: cfg.Predefined},
		{Name: "<builtin>", Value: Builtin},
		{Name: "test.h", Value: src},
	}

	ast, err := Translate(cfg, sources)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}

	d := ast.Scope.Nodes["i"][0].(*Declarator)
	t.Log(d.Type())
	if g, e := d.IsVolatile(), true; g != e {
		t.Error(g, e)
	}

	if g, e := d.Type().Attributes().IsVolatile(), true; g != e {
		t.Error(g, e)
	}
}

func TestVolatile2(t *testing.T) {
	const src = "volatile int *p;\n"
	cfg, err := NewConfig(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		t.Fatalf("failed to create new config: %v", err)
	}

	sources := []Source{
		{Name: "<predefined>", Value: cfg.Predefined},
		{Name: "<builtin>", Value: Builtin},
		{Name: "test.h", Value: src},
	}

	ast, err := Translate(cfg, sources)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}

	d := ast.Scope.Nodes["p"][0].(*Declarator)
	t.Log(d.Type())
	if g, e := d.IsVolatile(), false; g != e {
		t.Error(g, e)
	}

	if g, e := d.Type().Attributes().IsVolatile(), false; g != e {
		t.Error(g, e)
	}

	if g, e := d.Type().(*PointerType).Elem().Attributes().IsVolatile(), true; g != e {
		t.Error(g, e)
	}
}

func TestVolatile3(t *testing.T) {
	const src = "int * volatile p;\n"
	cfg, err := NewConfig(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		t.Fatalf("failed to create new config: %v", err)
	}

	sources := []Source{
		{Name: "<predefined>", Value: cfg.Predefined},
		{Name: "<builtin>", Value: Builtin},
		{Name: "test.h", Value: src},
	}

	ast, err := Translate(cfg, sources)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}

	d := ast.Scope.Nodes["p"][0].(*Declarator)
	t.Log(d.Type())
	if g, e := d.IsVolatile(), true; g != e {
		t.Error(g, e) //TODO
	}

	if g, e := d.Type().Attributes().IsVolatile(), true; g != e {
		t.Error(g, e) //TODO
	}

	if g, e := d.Type().(*PointerType).Elem().Attributes().IsVolatile(), false; g != e {
		t.Error(g, e)
	}
}

func TestVolatile4(t *testing.T) {
	const src = "volatile int * volatile p;\n"
	cfg, err := NewConfig(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		t.Fatalf("failed to create new config: %v", err)
	}

	sources := []Source{
		{Name: "<predefined>", Value: cfg.Predefined},
		{Name: "<builtin>", Value: Builtin},
		{Name: "test.h", Value: src},
	}

	ast, err := Translate(cfg, sources)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}

	d := ast.Scope.Nodes["p"][0].(*Declarator)
	t.Log(d.Type())
	if g, e := d.IsVolatile(), true; g != e {
		t.Error(g, e)
	}

	if g, e := d.Type().Attributes().IsVolatile(), true; g != e {
		t.Error(g, e)
	}

	if g, e := d.Type().(*PointerType).Elem().Attributes().IsVolatile(), true; g != e {
		t.Error(g, e)
	}
}

func TestVolatile5(t *testing.T) {
	const src = "volatile int *p[];\n"
	cfg, err := NewConfig(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		t.Fatalf("failed to create new config: %v", err)
	}

	sources := []Source{
		{Name: "<predefined>", Value: cfg.Predefined},
		{Name: "<builtin>", Value: Builtin},
		{Name: "test.h", Value: src},
	}

	ast, err := Translate(cfg, sources)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}

	d := ast.Scope.Nodes["p"][0].(*Declarator)
	t.Log(d.Type())
	if g, e := d.IsVolatile(), false; g != e {
		t.Error(g, e)
	}

	if g, e := d.Type().Attributes().IsVolatile(), false; g != e {
		t.Error(g, e)
	}

	if g, e := d.Type().(*ArrayType).Elem().Attributes().IsVolatile(), true; g != e {
		t.Error(g, e)
	}
}

func TestBindDeclarations(t *testing.T) {
	const src = "int x __attribute__((addr(42)));"
	cfg, err := NewConfig(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		t.Fatalf("failed to create new config: %v", err)
	}

	sources := []Source{
		{Name: "<predefined>", Value: cfg.Predefined},
		{Name: "<builtin>", Value: Builtin},
		{Name: "test.h", Value: src},
	}

	ast, err := Translate(cfg, sources)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}

	var found bool
	for _, v := range ast.Scope.Nodes["x"] {
		d, ok := v.(*Declarator)
		if !ok {
			continue
		}
		found = true
		attrs := d.Type().Attributes()
		t.Logf("%T: %v", v, d.Type().Attributes())

		if !attrs.IsAttrSet("addr") {
			t.Fatalf("attrs.IsAttrSet(\"addr\") failed")
		}
		attrval := attrs.AttrValue("addr")
		if attrval == nil {
			t.Fatalf("attrs.AttrValue(\"addr\") returned nil")
		}
		if len(attrval) != 1 {
			t.Fatalf("attrs.AttrValue(\"addr\") returned slice of wrong length")
		}
		t.Log(attrval)
		intval, ok := attrval[0].(Int64Value)
		if !ok {
			t.Fatalf("attrs.AttrValue(\"addr\") returned wrong type: %T", attrval[0])
		}
		if intval != 42 {
			t.Fatalf("attrs.AttrValue(\"addr\") returned wrong value")
		}
	}
	if !found {
		t.Fatalf("Could not find node associated with x")
	}
}

func TestPointerIntComparison(t *testing.T) {
	const src = "void f(){int x = ((char *)1) == 1;}\n"
	cfg, err := NewConfig(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		t.Fatalf("failed to create new config: %v", err)
	}

	sources := []Source{
		{Name: "<predefined>", Value: cfg.Predefined},
		{Name: "<builtin>", Value: Builtin},
		{Name: "test.h", Value: src},
	}

	_, err = Translate(cfg, sources)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
}

func TestRandomError(t *testing.T) {
	const src = "void f(){struct T{unsigned char x[1];};(struct T){0};}\n"
	cfg, err := NewConfig(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		t.Fatalf("failed to create new config: %v", err)
	}

	sources := []Source{
		{Name: "<predefined>", Value: cfg.Predefined},
		{Name: "<builtin>", Value: `int __predefined_declarator;`}, // Just to test without wchar_t definition
		{Name: "test.h", Value: src},
	}

	if _, err = Translate(cfg, sources); err == nil {
		t.Fatalf("expected Translate error: %v", err)
	}
}

func TestRandomError2(t *testing.T) {
	const src = "void f(){struct T{unsigned char x[1];};(struct T){0};}\n"
	cfg, err := NewConfig(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		t.Fatalf("failed to create new config: %v", err)
	}

	sources := []Source{
		{Name: "<predefined>", Value: cfg.Predefined},
		{Name: "<builtin>", Value: `int __predefined_declarator;`}, // Just to test without wchar_t definition
		{Name: "test.h", Value: src},
	}

	cfg.DefaultSizeT = ULongLong
	cfg.DefaultWcharT = Int
	if _, err = Translate(cfg, sources); err != nil {
		t.Fatalf("unexpected Translate error: %v", err)
	}
}

func TestAttrValue(t *testing.T) {
	const src = `
char a1 __attribute__((b1(100)));
__attribute__((b2(200))) short a2;
int *a3 __attribute__((b3(300)));
__attribute__((b4(400))) long *a4;
`
	cfg, err := NewConfig(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		t.Fatalf("failed to create new config: %v", err)
	}

	sources := []Source{
		{Name: "<predefined>", Value: cfg.Predefined},
		{Name: "<builtin>", Value: Builtin},
		{Name: "test.h", Value: src},
	}

	ast, err := Translate(cfg, sources)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}

	d := ast.Scope.Nodes["a1"][0].(*Declarator)
	a := d.Type().Attributes()
	vs := a.AttrValue("b1")
	if len(vs) != 1 || vs[0] != Int64Value(100) {
		t.Fatal(vs)
	}

	d = ast.Scope.Nodes["a2"][0].(*Declarator)
	a = d.Type().Attributes()
	vs = a.AttrValue("b2")
	if len(vs) != 1 || vs[0] != Int64Value(200) {
		t.Fatal(vs)
	}

	d = ast.Scope.Nodes["a3"][0].(*Declarator)
	a = d.Type().Attributes()
	vs = a.AttrValue("b3")
	if len(vs) != 1 || vs[0] != Int64Value(300) {
		t.Fatal(vs)
	}

	a = d.Type().(*PointerType).Elem().Attributes()
	vs = a.AttrValue("b3")
	if len(vs) != 0 {
		t.Fatal(vs)
	}

	d = ast.Scope.Nodes["a4"][0].(*Declarator)
	a = d.Type().Attributes()
	vs = a.AttrValue("b4")
	if len(vs) != 0 {
		t.Fatal(vs)
	}

	a = d.Type().(*PointerType).Elem().Attributes()
	vs = a.AttrValue("b4")
	if len(vs) != 1 || vs[0] != Int64Value(400) {
		t.Fatal(vs)
	}
}

func TestCanonicalizeStrlitTail(t *testing.T) {
	for i, test := range []struct{ a, b string }{
		{``, ``},
		{`a`, `a`},
		{`\a`, `\a`},
		{`\-`, `\-`},
		{`\x1`, `\x01`},
		{`\x12`, `\x12`},
		{`\x123`, `\x123`},
		{`\0`, `\000`},
		{`\01`, `\001`},
		{`\0123`, `\0123`},
	} {
		if g, e := string(canonicalizeStrlitTail([]byte(test.a))), test.b; g != e {
			t.Errorf("%v: %q -> %q, want %q", i, test.a, g, e)
		}
	}
}

func TestVaArgs1(t *testing.T) {
	//	parser.go:265:shift: TRC bug.c:7:9: U+E030 "g"
	//	parser.go:265:shift: TRC bug.c:7:9: U+0028 '(' "("
	//	parser.go:265:shift: TRC bug.c:7:9: U+E054 "\"foo\""
	//	parser.go:265:shift: TRC bug.c:7:9: U+002C ',' ","	<--- comma not deleted
	//	parser.go:265:shift: TRC bug.c:7:9: U+0029 ')' ")"
	//	parser.go:265:shift: TRC bug.c:7:17: U+003B ';' ";"
	//	parser.go:265:shift: TRC bug.c:8:1: U+007D '}' "}"
	//
	//	bug.c:7:9: unexpected ')', expected unary expression
	//
	//	https://gcc.gnu.org/onlinedocs/gcc-4.9.4/cpp/Variadic-Macros.html
	cfg := defaultCfg()
	cfg.noPredefinedDeclarator = true
	_, err := Parse(cfg, []Source{
		{Name: "bug.c", Value: `
#define m(a, ...) g(a, ##__VA_ARGS__)

void g() {}

void f() {
        m("foo");
}
`},
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestVaArgs2(t *testing.T) {
	//	parser.go:265:shift: TRC bug.c:7:9: U+E030 "g"
	//	parser.go:265:shift: TRC bug.c:7:9: U+0028 '(' "("
	//	parser.go:265:shift: TRC bug.c:7:9: U+E054 "\"foo\""
	//	parser.go:265:shift: TRC bug.c:7:9: U+002C ',' ",42"	<--- malformed token
	//	parser.go:265:shift: TRC bug.c:7:9: U+0029 ')' ")"
	//	parser.go:265:shift: TRC bug.c:7:21: U+003B ';' ";"
	//	parser.go:265:shift: TRC bug.c:8:1: U+007D '}' "}"
	//
	//	bug.c:7:9: unexpected ')', expected unary expression
	cfg := defaultCfg()
	cfg.noPredefinedDeclarator = true
	_, err := Parse(cfg, []Source{
		{Name: "bug.c", Value: `
#define m(a, ...) g(a, ##__VA_ARGS__)

void g() {}

void f() {
        m("foo", 42);
}
`},
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestSingleBytewchar(t *testing.T) {
	const src = `
#include <stddef.h>
wchar_t *c = L"!";
`
	cfg, err := NewConfig(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		t.Fatalf("failed to create new config: %v", err)
	}
	sources := []Source{
		{Name: "<predefined>", Value: cfg.Predefined},
		{Name: "<predefined2>", Value: `
#define __WCHAR_MAX__ 0xff
#define __WCHAR_MIN__ 0
#define __WCHAR_WIDTH__ 8
#define __WCHAR_TYPE__ unsigned char
#define __SIZEOF_WCHAR_T__ 1
`},
		{Name: "<builtin>", Value: Builtin},
		{Name: "test.h", Value: src},
	}
	var ast *AST
	if ast, err = Translate(cfg, sources); err != nil {
		t.Fatalf("unexpected Translate error: %v", err)
	}
	nodes := findAllNodes("InitDeclarator", ast.TranslationUnit)
	for _, n := range nodes {
		decl := n.(*InitDeclarator)
		if decl.Declarator.Name() == "c" {
			var out Node
			d := 100
			findNode("PrimaryExpression", decl, 0, &out, &d)
			pe := out.(*PrimaryExpression)
			if _, ok := pe.Value().(StringValue); !ok {
				t.Fatalf("value should be of type StringValue, but was of type %T", pe.Value())
			}
			return
		}
	}
	t.Fatalf("Could not find InitDeclarator for value 'c'")
}

// https://gitlab.com/cznic/ccgo/-/issues/34
func TestIssue34(t *testing.T) {
	cfg := defaultCfg()
	_, err := Translate(cfg, []Source{
		{Name: "<predefined>", Value: cfg.Predefined},
		{Name: "<builtin>", Value: Builtin},
		{Name: "bug.c", Value: `

void* f() {
	return 0;
}

void* g() {
	return &(((int*)(f()))[1]);
} 

`},
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestIssue34b(t *testing.T) {
	cfg := defaultCfg()
	_, err := Translate(cfg, []Source{
		{Name: "<predefined>", Value: cfg.Predefined},
		{Name: "<builtin>", Value: Builtin},
		{Name: "bug.c", Value: `

typedef unsigned long int __uint64_t;
typedef __uint64_t uint64_t;
typedef uint64_t ecs_id_t;
typedef ecs_id_t ecs_entity_t;

typedef enum ecs_primitive_kind_t {
 EcsBool = 1,
 EcsChar,
 EcsByte,
 EcsU8,
 EcsU16,
 EcsU32,
 EcsU64,
 EcsI8,
 EcsI16,
 EcsI32,
 EcsI64,
 EcsF32,
 EcsF64,
 EcsUPtr,
 EcsIPtr,
 EcsString,
 EcsEntity,
 EcsId,
 EcsPrimitiveKindLast = EcsId
} ecs_primitive_kind_t;

typedef struct ecs_primitive_desc_t {
 ecs_entity_t entity; 
 ecs_primitive_kind_t kind;
} ecs_primitive_desc_t;

// ----------------------------------------------------------------------------

typedef unsigned long int __uint64_t;
typedef __uint64_t uint64_t;
typedef uint64_t ecs_id_t;
typedef ecs_id_t ecs_entity_t;

typedef enum ecs_primitive_kind_t {
 EcsBool = 1,
 EcsChar,
 EcsByte,
 EcsU8,
 EcsU16,
 EcsU32,
 EcsU64,
 EcsI8,
 EcsI16,
 EcsI32,
 EcsI64,
 EcsF32,
 EcsF64,
 EcsUPtr,
 EcsIPtr,
 EcsString,
 EcsEntity,
 EcsId,
 EcsPrimitiveKindLast = EcsId
} ecs_primitive_kind_t;

typedef struct ecs_primitive_desc_t {
 ecs_entity_t entity; 
 ecs_primitive_kind_t kind;
} ecs_primitive_desc_t;

`},
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestIssue34c(t *testing.T) {
	cfg := defaultCfg()
	_, err := Translate(cfg, []Source{
		{Name: "<predefined>", Value: cfg.Predefined},
		{Name: "<builtin>", Value: Builtin},
		{Name: "bug.c", Value: `

typedef struct ecs_world_t ecs_world_t;
typedef unsigned long int __uint64_t;
typedef __uint64_t uint64_t;
typedef uint64_t ecs_id_t;
typedef ecs_id_t ecs_entity_t;
typedef struct ecs_serializer_t {
 
 int (*value)(
 const struct ecs_serializer_t *ser, 
 ecs_entity_t type, 
 const void *value); 

 
 int (*member)(
 const struct ecs_serializer_t *ser, 
 const char *member); 

 const ecs_world_t *world;
 void *ctx;
} ecs_serializer_t;

// ----------------------------------------------------------------------------

typedef struct ecs_world_t ecs_world_t;
typedef unsigned long int __uint64_t;
typedef __uint64_t uint64_t;
typedef uint64_t ecs_id_t;
typedef ecs_id_t ecs_entity_t;
typedef struct ecs_serializer_t {
 
 int (*value)(
 const struct ecs_serializer_t *ser, 
 ecs_entity_t type, 
 const void *value); 

 
 int (*member)(
 const struct ecs_serializer_t *ser, 
 const char *member); 

 const ecs_world_t *world;
 void *ctx;
} ecs_serializer_t;

`},
	})
	if err != nil {
		t.Fatal(err)
	}
}

// https://gitlab.com/cznic/cc/-/issues/165
func TestIssue165(t *testing.T) {
	const src = `
#define CEPH_RBD_API          __attribute__ ((visibility ("default")))
#define CEPH_RBD_DEPRECATED   __attribute__((deprecated))
CEPH_RBD_API int a CEPH_RBD_DEPRECATED; 
`
	cfg, err := NewConfig(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		t.Fatalf("failed to create new config: %v", err)
	}

	sources := []Source{
		{Name: "<predefined>", Value: cfg.Predefined},
		{Name: "<builtin>", Value: Builtin},
		{Name: "test.c", Value: src},
	}

	ast, err := Translate(cfg, sources)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}

	d := ast.Scope.Nodes["a"][0].(*Declarator)
	for _, v := range []string{"visibility", "deprecated"} {
		if g, e := d.Type().Attributes().IsAttrSet(v), true; g != e {
			t.Errorf("%s: got=%v expexted=%v", v, g, e)
		}
	}
}

func TestEnumC23(t *testing.T) {
	const src = `
enum {a2};
enum b3;
enum c4 {d4};
enum : char {e5};
enum f6 : char;
enum g7 : char {h7};
`
	cfg, err := NewConfig(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		t.Fatalf("failed to create new config: %v", err)
	}

	sources := []Source{
		{Name: "<predefined>", Value: cfg.Predefined},
		{Name: "<builtin>", Value: Builtin},
		{Name: "test.c", Value: src},
	}

	if _, err := Translate(cfg, sources); err != nil {
		t.Fatalf("parse error: %v", err)
	}
}

// __bf16 is clang's/GCC's bfloat16 type. Apple clang's <emmintrin.h> typedefs
// it, so failing to recognize it made every SSE2-using translation unit
// unparseable on darwin/amd64.
func TestBFloat16(t *testing.T) {
	for _, tc := range []struct {
		goos, goarch string
		src          string
		size         int64
		align        int
	}{
		{"darwin", "amd64", "__bf16 x;", 2, 2},
		{"darwin", "arm64", "__bf16 x;", 2, 2},
		// The <emmintrin.h> form that motivated the support.
		{"darwin", "amd64", "typedef __bf16 __v8bf __attribute__((__vector_size__(16), __aligned__(16)));\n__v8bf x;", 16, 16},
	} {
		cfg, err := NewConfig(tc.goos, tc.goarch, LongDouble64Flag(tc.goos, tc.goarch))
		if err != nil {
			t.Fatalf("%s/%s: %v", tc.goos, tc.goarch, err)
		}

		ast, err := Translate(cfg, []Source{
			{Name: "<predefined>", Value: cfg.Predefined},
			{Name: "<builtin>", Value: Builtin},
			{Name: "t.c", Value: tc.src},
		})
		if err != nil {
			t.Errorf("%s/%s: %q: %v", tc.goos, tc.goarch, tc.src, err)
			continue
		}

		nodes := ast.Scope.Nodes["x"]
		if len(nodes) == 0 {
			t.Errorf("%s/%s: %q: x not declared", tc.goos, tc.goarch, tc.src)
			continue
		}

		d, ok := nodes[0].(*Declarator)
		if !ok {
			t.Errorf("%s/%s: %q: got %T, expected *Declarator", tc.goos, tc.goarch, tc.src, nodes[0])
			continue
		}

		typ := d.Type()
		if g, e := typ.Kind(), BFloat16; g != e {
			t.Errorf("%s/%s: %q: kind %v, expected %v", tc.goos, tc.goarch, tc.src, g, e)
		}
		if g, e := typ.Size(), tc.size; g != e {
			t.Errorf("%s/%s: %q: size %v, expected %v", tc.goos, tc.goarch, tc.src, g, e)
		}
		if g, e := typ.Align(), tc.align; g != e {
			t.Errorf("%s/%s: %q: align %v, expected %v", tc.goos, tc.goarch, tc.src, g, e)
		}
	}
}

// A target without __bf16 in its ABI table must report that cleanly instead of
// failing to parse, same as _Float16 does.
func TestBFloat16Unsupported(t *testing.T) {
	cfg, err := NewConfig("linux", "amd64", LongDouble64Flag("linux", "amd64"))
	if err != nil {
		t.Fatal(err)
	}

	switch _, err = Translate(cfg, []Source{
		{Name: "<predefined>", Value: cfg.Predefined},
		{Name: "<builtin>", Value: Builtin},
		{Name: "t.c", Value: "__bf16 x;"},
	}); {
	case err == nil:
		t.Fatal("unexpected success")
	case !strings.Contains(err.Error(), "__bf16 not supported on linux/amd64"):
		t.Fatalf("unexpected error: %v", err)
	}
}

// With EvalAllMacros, an object-like macro whose expansion contained sizeof, a
// cast, an enumeration constant or any other identifier that is not a macro
// was evaluated by the preprocessor's #if evaluator, which treats identifiers
// as 0, so 'sizeof(T)*N' became 0 and '(N-8)/sizeof(T)' N-8; ccgo then emitted
// const WALINDEX_PGSZ = 0 for SQLite. Found by hazyhaar (cznic/sqlite#221,
// https://gitlab.com/cznic/libsqlite3/-/merge_requests/4).
func TestEvalAllMacros(t *testing.T) {
	cfg := defaultCfg()
	cfg.EvalAllMacros = true
	ast, err := Translate(cfg, []Source{
		{Name: "<predefined>", Value: cfg.Predefined},
		{Name: "<builtin>", Value: Builtin},
		{Name: "macros.c", Value: `
typedef unsigned char u8;
typedef unsigned short u16;
typedef unsigned int u32;
typedef u16 ht_slot;

typedef struct WalIndexHdr {
	u32 iVersion;
	u32 unused;
	u32 iChange;
	u8  isInit;
	u8  big_endian;
	u16 szPage;
	u32 mxFrame;
	u32 nPage;
	u32 aFrameCksum[2];
	u32 aSalt[2];
	u32 aCksum[2];
} WalIndexHdr;

struct RowSetEntry { u32 v; u32 a; u32 b; };

enum { E5 = 5 };
int f(int);

#define HASHTABLE_NPAGE      4096
#define HASHTABLE_NSLOT      8192
#define WALINDEX_HDR_SIZE    ((sizeof(WalIndexHdr) + 7) & ~7)
#define WALINDEX_PGSZ        (sizeof(ht_slot)*HASHTABLE_NSLOT + HASHTABLE_NPAGE*sizeof(u32))
#define HASHTABLE_NPAGE_ONE  (HASHTABLE_NPAGE - (WALINDEX_HDR_SIZE/sizeof(u32)))
#define ROWSET_ENTRY_PER_CHUNK ((1024-8)/sizeof(struct RowSetEntry))
#define EXPR_FULLSIZE        sizeof(WalIndexHdr)
#define BMS                  ((int)(sizeof(u32)*8))
#define EPLUS                (E5 + 1)
#define LIT                  (1 + 2)
#define NEG                  (-1)
#define CH                   'a'
#define FLT                  1.5
#define FLTX                 (1.5 * 2)
#define STR                  "a" "b"
#define UNDECLARED           (nosuch + 1)
#define CALL                 f(1)
#define TYPE                 unsigned int
#define SELF                 SELF
#define EMPTY
`},
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name string
		want Value
	}{
		{"WALINDEX_HDR_SIZE", UInt64Value(48)},
		{"WALINDEX_PGSZ", UInt64Value(32768)},
		{"HASHTABLE_NPAGE_ONE", UInt64Value(4084)},
		{"ROWSET_ENTRY_PER_CHUNK", UInt64Value(84)},
		{"EXPR_FULLSIZE", UInt64Value(48)},
		{"BMS", Int64Value(32)},
		{"EPLUS", Int64Value(6)},
		{"LIT", Int64Value(3)},
		{"NEG", Int64Value(-1)},
		{"CH", Int64Value('a')},
		{"FLT", Float64Value(1.5)},
		{"FLTX", Unknown}, // floating point arithmetic is not folded
		{"STR", StringValue("ab\x00")},
		{"UNDECLARED", Unknown},
		{"CALL", Unknown},
		{"TYPE", Unknown},
		{"SELF", Unknown},
		{"EMPTY", Unknown},
	} {
		m := ast.Macros[tc.name]
		if m == nil {
			t.Errorf("%s: macro not found", tc.name)
			continue
		}

		if g, e := m.Value(), tc.want; g != e {
			t.Errorf("%s: got %v (%T), want %v (%T)", tc.name, g, g, e, e)
		}
	}
}

// TestC23 covers the C23 spellings of keywords and the identifiers C23 turned
// into keywords. gcc 15 defaults to C23, and its glibc's <assert.h>,
// <stdbool.h> and <stddef.h> then define no macro for them any more, which made
// the loong64 builder fail on testdata/bug/10.c (static_assert) with gcc 15.3.
func TestC23(t *testing.T) {
	cfg, err := NewConfig(runtime.GOOS, runtime.GOARCH, "-std=gnu23")
	if err != nil {
		t.Skipf("no C23 host compiler: %v", err)
	}

	if v := stdcVersion(cfg.Predefined); v < 202000 {
		t.Skipf("__STDC_VERSION__ %v", v)
	}

	if _, err := Translate(cfg, []Source{
		{Name: "<predefined>", Value: cfg.Predefined},
		{Name: "<builtin>", Value: Builtin},
		{Name: "testdata/bug/10.c"},
	}); err != nil {
		t.Error(err)
	}

	ast, err := Translate(cfg, []Source{
		{Name: "<predefined>", Value: cfg.Predefined},
		{Name: "<builtin>", Value: Builtin},
		{Name: "c23.c", Value: `
#include <assert.h>
#include <stdbool.h>
#include <stddef.h>

static_assert(sizeof(int) >= 2, "int");
alignas(16) static int a[4];
static thread_local int tl;
bool b = true, c = false;
int *p = nullptr;
int n = alignof(int);
int m = sizeof(a);
`},
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name string
		kind Kind
	}{
		{"b", Bool},
		{"c", Bool},
		{"p", Ptr},
	} {
		d, ok := ast.Scope.Nodes[tc.name][0].(*Declarator)
		if !ok {
			t.Errorf("%s: not found", tc.name)
			continue
		}

		if g, e := d.Type().Kind(), tc.kind; g != e {
			t.Errorf("%s: kind %v, want %v", tc.name, g, e)
		}
	}

	// Below C23 the identifiers stay identifiers.
	cfg = defaultCfg()
	if stdcVersion(cfg.Predefined) >= 202000 {
		return
	}

	if _, err := Translate(cfg, []Source{
		{Name: "<predefined>", Value: cfg.Predefined},
		{Name: "<builtin>", Value: Builtin},
		{Name: "c17.c", Value: "int bool = 1, true = 1, false = 0, nullptr = 0, alignas = 2, static_assert = 3;\n"},
	}); err != nil {
		t.Error(err)
	}
}

func TestIssue166(t *testing.T) {
	// https://gitlab.com/cznic/cc/-/issues/166
	const src = `
enum opcode { PROTO = '\x80', FRAME = '\x95', FILL = '\xff', OCT = '\200', A = 'a', W = L'\x80', W2 = L'\xff' };
#if '\x80' < 0
int negative;
#else
int positive;
#endif
`
	for _, signedChar := range []bool{true, false} {
		cfg := defaultCfg()
		abi := *cfg.ABI
		abi.SignedChar = signedChar
		cfg.ABI = &abi
		ast, err := Translate(cfg, []Source{
			{Name: "<predefined>", Value: cfg.Predefined},
			{Name: "<builtin>", Value: Builtin},
			{Name: "test.c", Value: src},
		})
		if err != nil {
			t.Fatal(err)
		}

		exp := map[string]int64{"PROTO": 128, "FRAME": 149, "FILL": 255, "OCT": 128, "A": 'a', "W": 128, "W2": 255}
		declared, undeclared := "positive", "negative"
		if signedChar {
			exp["PROTO"], exp["FRAME"], exp["FILL"], exp["OCT"] = -128, -107, -1, -128
			declared, undeclared = "negative", "positive"
		}
		for nm, e := range exp {
			en, ok := ast.Scope.Nodes[nm][0].(*Enumerator)
			if !ok {
				t.Errorf("signed char %v: %s: not an enumerator", signedChar, nm)
				continue
			}

			var g int64
			switch x := en.Value().(type) {
			case Int64Value:
				g = int64(x)
			case UInt64Value:
				g = int64(x)
			default:
				t.Errorf("signed char %v: %s: unexpected value %T(%[3]v)", signedChar, nm, x)
				continue
			}
			if g != e {
				t.Errorf("signed char %v: %s: got %v, want %v", signedChar, nm, g, e)
			}
		}
		if _, ok := ast.Scope.Nodes[declared]; !ok {
			t.Errorf("signed char %v: #if '\\x80' < 0 evaluated wrongly, %q not declared", signedChar, declared)
		}
		if _, ok := ast.Scope.Nodes[undeclared]; ok {
			t.Errorf("signed char %v: #if '\\x80' < 0 evaluated wrongly, %q declared", signedChar, undeclared)
		}
	}
}

// testInitializer returns the initializer of the file scope object nm.
func testInitializer(ast *AST, nm string) *Initializer {
	for tu := ast.TranslationUnit; tu != nil; tu = tu.TranslationUnit {
		ed := tu.ExternalDeclaration
		if ed == nil || ed.Case != ExternalDeclarationDecl {
			continue
		}

		for l := ed.Declaration.InitDeclaratorList; l != nil; l = l.InitDeclaratorList {
			if id := l.InitDeclarator; id.Initializer != nil && id.Declarator.Name() == nm {
				return id.Initializer
			}
		}
	}
	return nil
}

// testInitializerLeaves returns "offset:value:len" for every expression
// initializer within n, in source order.
func testInitializerLeaves(n *Initializer, a *[]string) {
	switch n.Case {
	case InitializerExpr:
		var v interface{} = n.Value()
		switch x := v.(type) {
		case Int64Value:
			v = int64(x)
		case UInt64Value:
			v = uint64(x)
		}
		*a = append(*a, fmt.Sprintf("%d:%v:%d", n.Offset(), v, n.Len()))
	case InitializerInitList:
		for l := n.InitializerList; l != nil; l = l.InitializerList {
			testInitializerLeaves(l.Initializer, a)
		}
	}
}

func TestIssue167(t *testing.T) {
	// https://gitlab.com/cznic/cc/-/issues/167
	const src = `
typedef unsigned short uint16_t;
typedef unsigned char uint8_t;
typedef union { uint16_t cache; struct { uint8_t code; uint8_t arg; } op; } CodeUnit;
static const CodeUnit insts[] = {
	{ .op.code = 1, .op.arg = 2 },
	{ .op.code = 3, .op.arg = 4 },
};
struct S { CodeUnit u; int i; };
struct S s = { .u.op.code = 5, .u.op.arg = 6, .i = 7 };
CodeUnit b[] = { [0].op.code = 1, [1].op.arg = 2 };
int a2[][3] = { [1][2] = 5 };
int r[] = { [0 ... 3] = 1, 2 };
int r2[8] = { [0 ... 3] = 1, 2 };
struct R3 { int x; } r3[8] = { [1][2] = 1 };
`
	cfg := defaultCfg()
	_, err := Translate(cfg, []Source{
		{Name: "<predefined>", Value: cfg.Predefined},
		{Name: "<builtin>", Value: Builtin},
		{Name: "test.c", Value: src},
	})
	if err == nil {
		t.Fatal("expected an error for r3")
	}

	if !strings.Contains(err.Error(), "test.c:15:") || !strings.Contains(err.Error(), "expected field name") || strings.Contains(err.Error(), "MISSING") {
		t.Fatal(err)
	}

	ast, err := Translate(cfg, []Source{
		{Name: "<predefined>", Value: cfg.Predefined},
		{Name: "<builtin>", Value: Builtin},
		{Name: "test.c", Value: strings.Replace(src, "struct R3 { int x; } r3[8] = { [1][2] = 1 };\n", "", 1)},
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		nm     string
		leaves string
		len    int64
	}{
		{"insts", "0:1:1 1:2:1 2:3:1 3:4:1", 2},
		{"s", "0:5:1 1:6:1 4:7:1", -1},
		{"b", "0:1:1 3:2:1", 2},
		{"a2", "20:5:1", 2},
		{"r", "0:1:4 16:2:1", 5},
		{"r2", "0:1:4 16:2:1", 8},
	} {
		in := testInitializer(ast, tc.nm)
		if in == nil {
			t.Errorf("%s: initializer not found", tc.nm)
			continue
		}

		var a []string
		testInitializerLeaves(in, &a)
		if g, e := strings.Join(a, " "), tc.leaves; g != e {
			t.Errorf("%s: got leaves %q, want %q", tc.nm, g, e)
		}
		if tc.len < 0 {
			continue
		}

		x, ok := ast.Scope.Nodes[tc.nm][0].(*Declarator).Type().(*ArrayType)
		if !ok {
			t.Errorf("%s: not an array", tc.nm)
			continue
		}

		if g, e := x.Len(), tc.len; g != e {
			t.Errorf("%s: got len %v, want %v", tc.nm, g, e)
		}
	}

	// Both items of a union initializer list select the same member.
	for i, l := 0, testInitializer(ast, "insts").InitializerList; l != nil; i, l = i+1, l.InitializerList {
		for m := l.Initializer.InitializerList; m != nil; m = m.InitializerList {
			if f := m.UnionField(); f == nil || f.Name() != "op" {
				t.Errorf("insts[%d]: union field %v, want op", i, f)
			}
		}
	}
}

func TestIssue168(t *testing.T) {
	// https://gitlab.com/cznic/cc/-/issues/168
	const src = `
struct H { int a; };
static struct H handler_info[64];
static struct H handler_info[] = { {1}, {2}, {0} };
int n = sizeof handler_info / sizeof handler_info[0];
extern int x[];
int x[] = { 1, 2, 3 };
`
	cfg := defaultCfg()
	ast, err := Translate(cfg, []Source{
		{Name: "<predefined>", Value: cfg.Predefined},
		{Name: "<builtin>", Value: Builtin},
		{Name: "test.c", Value: src},
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		nm  string
		len int64
	}{
		{"handler_info", 64},
		{"x", 3},
	} {
		for i, v := range ast.Scope.Nodes[tc.nm] {
			x, ok := v.(*Declarator).Type().(*ArrayType)
			if !ok {
				t.Errorf("%s#%d: not an array", tc.nm, i)
				continue
			}

			if g, e := x.Len(), tc.len; g != e {
				t.Errorf("%s#%d: len %v, want %v", tc.nm, i, g, e)
			}
		}
	}
	if in := testInitializer(ast, "handler_info"); in == nil || in.Type().(*ArrayType).Len() != 64 {
		t.Errorf("handler_info: initializer type %v, want array of 64", in.Type())
	}
	if g, e := testInitializer(ast, "n").Value(), Int64Value(64); g != e {
		t.Errorf("n: %v, want %v", g, e)
	}

	// The reverse order is a conflict, as in gcc and clang.
	if _, err = Translate(cfg, []Source{
		{Name: "<predefined>", Value: cfg.Predefined},
		{Name: "<builtin>", Value: Builtin},
		{Name: "test.c", Value: "struct H { int a; };\nstatic struct H b[] = { {1} };\nstatic struct H b[64];\n"},
	}); err == nil || !strings.Contains(err.Error(), "conflicting types") {
		t.Fatalf("got %v, want a conflicting types error", err)
	}
}

// testWalk calls f for every node reachable from n via exported fields.
func testWalk(n Node, f func(Node)) {
	seen := map[uintptr]bool{}
	var walk func(v reflect.Value)
	walk = func(v reflect.Value) {
		switch v.Kind() {
		case reflect.Ptr:
			if v.IsNil() || v.Elem().Kind() != reflect.Struct {
				return
			}

			if p := v.Pointer(); seen[p] {
				return
			} else {
				seen[p] = true
			}
			if x, ok := v.Interface().(Node); ok {
				f(x)
			}
			walk(v.Elem())
		case reflect.Interface:
			if !v.IsNil() {
				walk(v.Elem())
			}
		case reflect.Struct:
			t := v.Type()
			for i := 0; i < v.NumField(); i++ {
				if t.Field(i).PkgPath == "" {
					walk(v.Field(i))
				}
			}
		}
	}
	walk(reflect.ValueOf(n))
}

func TestIssue169(t *testing.T) {
	// https://gitlab.com/cznic/cc/-/issues/169
	const src = `
typedef int T;
int f(int x) {
	switch (x) {
	case 1:
		int y = 2;
		return y;
	case 2: T z = 3; return z;
	case 3: static const T w = 4; return w;
	case 4: a: b: T v = 5; return v;
	default:
		T *p = 0; return !p;
	}
	l: __attribute__((unused)) int q = 6;
	m:
	return q;
}
`
	cfg := defaultCfg()
	ast, err := Translate(cfg, []Source{
		{Name: "<predefined>", Value: cfg.Predefined},
		{Name: "<builtin>", Value: Builtin},
		{Name: "test.c", Value: src},
	})
	if err != nil {
		t.Fatal(err)
	}

	got := map[string]string{}
	testWalk(ast.TranslationUnit, func(n Node) {
		ls, ok := n.(*LabeledStatement)
		if !ok {
			return
		}

		var nm string
		switch ls.Case {
		case LabeledStatementLabel:
			nm = ls.Token.SrcStr()
		case LabeledStatementCaseLabel:
			nm = "case " + NodeSource(ls.ConstantExpression)
		case LabeledStatementDefault:
			nm = "default"
		}
		s := ls.Statement
		switch {
		case s == nil:
			got[nm] = "nil"
		case s.Case == StatementExpr && s.ExpressionStatement.ExpressionList == nil && s.ExpressionStatement.Token.Ch == ';':
			got[nm] = "empty"
		default:
			got[nm] = s.Case.String()
		}
	})
	exp := map[string]string{
		"case 1":  "empty",
		"case 2":  "empty",
		"case 3":  "empty",
		"case 4":  "StatementLabeled",
		"a":       "StatementLabeled",
		"b":       "empty",
		"default": "empty",
		"l":       "empty",
		"m":       "StatementJump",
	}
	if g, e := fmt.Sprint(got), fmt.Sprint(exp); g != e {
		t.Errorf("got %v\nwant %v", g, e)
	}

	// A declaration is not a statement; the extension only applies where a
	// block item is expected.
	if _, err := Translate(cfg, []Source{
		{Name: "<predefined>", Value: cfg.Predefined},
		{Name: "<builtin>", Value: Builtin},
		{Name: "test.c", Value: "int g(int x) { if (x) l: int y = 1; return 0; }\n"},
	}); err == nil || !strings.Contains(err.Error(), "expected statement") {
		t.Fatalf("got %v, want an expected statement error", err)
	}
}

func TestIssue170(t *testing.T) {
	// https://gitlab.com/cznic/cc/-/issues/170
	if runtime.GOOS == "windows" {
		t.Skip("fstest.MapFS uses slash separated paths")
	}

	cfg := defaultCfg()
	cfg.FS = fstest.MapFS{
		"a/limits.h": &fstest.MapFile{Data: []byte(`
#define A_LIMITS 1
#if __has_include_next(<limits.h>)
#include_next <limits.h>
#endif
#if __has_include_next("limits.h")
#define NEXT_QUOTED 1
#endif
#if __has_include_next(<nonexistent.h>)
#define NEXT_NONEXISTENT 1
#endif
#if __has_include(<limits.h>)
#define HAS_LIMITS 1
#endif
`)},
		"b/limits.h": &fstest.MapFile{Data: []byte("#define B_LIMITS 1\n")},
	}
	cfg.IncludePaths = nil
	cfg.SysIncludePaths = []string{"a", "b"}
	ast, err := Parse(cfg, []Source{{Name: "test.c", Value: `
#include <limits.h>
#ifdef __has_include
#define DEFINED0 1
#endif
#ifdef __has_include_next
#define DEFINED1 1
#endif
#if defined(__has_include_next) && __has_include_next(<limits.h>)
#define DEFINED2 1
#endif
int __predefined_declarator;
`}})
	if err != nil {
		t.Fatal(err)
	}

	for _, nm := range []string{"A_LIMITS", "B_LIMITS", "HAS_LIMITS", "NEXT_QUOTED", "DEFINED0", "DEFINED1", "DEFINED2"} {
		if ast.Macros[nm] == nil {
			t.Errorf("%s not defined", nm)
		}
	}
	if ast.Macros["NEXT_NONEXISTENT"] != nil {
		t.Error("NEXT_NONEXISTENT defined")
	}
}
