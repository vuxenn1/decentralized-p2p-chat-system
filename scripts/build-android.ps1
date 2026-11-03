# Builds the Android native binaries from the current core source.
# Requires the Android NDK. Set ANDROID_NDK_HOME or install the NDK under LOCALAPPDATA.
param(
    [string]$OutDir = "dist/android"
)

$ErrorActionPreference = "Stop"
$core = Resolve-Path (Join-Path $PSScriptRoot "..")
Push-Location $core

try {
    $ndk = $env:ANDROID_NDK_HOME
    if (-not $ndk) {
        $ndkRoot = Join-Path $env:LOCALAPPDATA "Android\Sdk\ndk"
        if (-not (Test-Path $ndkRoot)) {
            throw "Android NDK not found. Set ANDROID_NDK_HOME."
        }
        $ndk = (Get-ChildItem $ndkRoot -Directory | Sort-Object Name -Descending | Select-Object -First 1).FullName
    }

    $toolchain = Join-Path $ndk "toolchains\llvm\prebuilt\windows-x86_64\bin"
    $out = Join-Path $core $OutDir

    $targets = @(
        @{ Arch = "arm64"; Abi = "arm64-v8a"; Compiler = "aarch64-linux-android24-clang.cmd" },
        @{ Arch = "amd64"; Abi = "x86_64"; Compiler = "x86_64-linux-android24-clang.cmd" }
    )

    foreach ($target in $targets) {
        $cc = Join-Path $toolchain $target.Compiler
        if (-not (Test-Path $cc)) {
            throw "Compiler not found: $cc"
        }

        $dir = Join-Path $out $target.Abi
        New-Item -ItemType Directory -Force $dir | Out-Null

        $env:GOOS = "android"
        $env:GOARCH = $target.Arch
        $env:CGO_ENABLED = "1"
        $env:CC = $cc

        Write-Host "Building $($target.Abi)"
        go build -buildmode=pie -trimpath -ldflags="-s -w -checklinkname=0" -o (Join-Path $dir "libp2pchat.so") .
        if ($LASTEXITCODE -ne 0) {
            throw "Go build failed for $($target.Abi)"
        }

        $file = Get-Item (Join-Path $dir "libp2pchat.so")
        Write-Host ("Built {0} ({1:N1} MB)" -f $file.FullName, ($file.Length / 1MB))
    }
}
finally {
    Pop-Location
}
