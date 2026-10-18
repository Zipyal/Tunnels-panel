package main

import (
	"encoding/base64"
	"strings"
	"testing"
	"unicode/utf16"
)

// decode обратная операция к psEncode: base64 -> UTF-16LE -> строка
func decode(t *testing.T, s string) string {
	raw, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	u := make([]uint16, len(raw)/2)
	for i := range u {
		u[i] = uint16(raw[2*i]) | uint16(raw[2*i+1])<<8
	}
	return string(utf16.Decode(u))
}

func TestPsEncodeRoundTrip(t *testing.T) {
	in := "Write-Output 'Привет, Иван'"
	if got := decode(t, psEncode(in)); got != in {
		t.Fatalf("got %q", got)
	}
}

func TestInstallScriptContainsLogPathAndEscapes(t *testing.T) {
	outer := installScripts(`C:\Users\O'Neil\tunnels-data\install.log`)
	const marker = "'-EncodedCommand','"
	i := strings.Index(outer, marker)
	if i < 0 {
		t.Fatal("нет вложенной команды")
	}
	rest := outer[i+len(marker):]
	inner := decode(t, rest[:strings.Index(rest, "'")])
	if !strings.Contains(inner, `O''Neil`) { // одинарная кавычка в пути удвоена
		t.Fatalf("путь не экранирован:\n%s", inner)
	}
	if !strings.Contains(inner, "Add-WindowsCapability -Online -Name OpenSSH.Client~~~~0.0.1.0") {
		t.Fatal("нет команды установки")
	}
}

func TestExplainInstallError(t *testing.T) {
	if got := explainInstallError("ERR ... 0x800f0954 ..."); !strings.Contains(got, "Windows Update") {
		t.Fatalf("got %q", got)
	}
	if got := explainInstallError("ERR что-то другое"); !strings.HasSuffix(got, "что-то другое") {
		t.Fatalf("got %q", got)
	}
}
