[CmdletBinding()]
param (
    [Uri]$LatestUri = 'https://github.com/thezim/aicm/releases/latest',
    [string]$BinaryName = 'aicm'
)

# Set preferences.
$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'

function Get-OperatingSystem {
    if($IsLinux -or $IsMacOS){
        return (& uname).ToLower()
    }
    if($IsWindows -or $PSVersionTable.PSEdition -eq 'Desktop'){
        return 'windows'
    }
}

function Get-Architecture {
    if($IsLinux -or $IsMacOS){
        $rawarch = (& uname -m).ToLower()
    } else {
        $rawarch = (Get-CimInstance Win32_OperatingSystem).OSArchitecture
    }
    switch -regex ($rawarch) {
        'x86_64|64-bit' {
            return 'amd64'
        }
        'arm64|aarch64|ARM 64-bit Processor' {
            return 'arm64'
        }
        default {
            throw New-Object PlatformNotSupportedException("This application only supports 64-bit Intel/AMD (amd64) and ARM64 (arm64) architectures.")
        }
    }
}

# Get OS and architecture.
$os = Get-OperatingSystem
$arch = Get-Architecture

# Determine archive extension.
if($os -eq "windows"){
    $extension = '.zip'
} else {
    $extension = '.tar.gz'
}

# Get latest version download URL. Uses latest URL and captures the 302 to the actual version URL.
try {
    # PS 5.1 will not generate an exception for 302 but return the response.
    $response = Invoke-WebRequest -Uri $LatestUri -MaximumRedirection 0 -ErrorAction SilentlyContinue -UseBasicParsing
} catch {
    # PS 7+ will generate an exception for 302. Respsone is in exception.
    $response = $_.Exception.Response
}
$latestversion = ($response.Headers.Location.ToString() | Split-Path -Leaf).TrimStart('v')
$downloadurl = "https://github.com/thezim/aicm/releases/latest/download/aicm_$($latestversion)_$($os)_$($arch)$($extension)"

# Download the file.
$filename = $downloadurl | Split-Path -Leaf
$downloadspath = Join-Path -Path "$HOME" -ChildPath Downloads
$downloadfilename = Join-Path -Path $downloadspath -ChildPath $filename
Invoke-WebRequest -Uri $downloadurl -OutFile $downloadfilename

# Extract binary.
if($os -eq "windows"){
    Add-Type -AssemblyName System.IO.Compression.FileSystem
    $zip = [System.IO.Compression.ZipFile]::OpenRead($downloadfilename)
    $file = $zip.Entries | Where-Object { $_.Name -eq ("$BinaryName.exe") }
    $savepath = Join-Path -Path "$env:LOCALAPPDATA\Microsoft\WindowsApps" -ChildPath "$BinaryName.exe"
    [System.IO.Compression.ZipFileExtensions]::ExtractToFile($file, $savepath, $true)
    $zip.Dispose()
} else {
    $installpath = "$HOME/.local/bin"
    New-Item -Path $installpath -ItemType Directory -ErrorAction SilentlyContinue | Out-Null
    $savepath = Join-Path -Path $installpath -ChildPath $BinaryName
    $filestream = [System.IO.File]::OpenRead($downloadfilename)
    $gzipstream = [System.IO.Compression.GZipStream]::new($filestream, [System.IO.Compression.CompressionMode]::Decompress)
    $tarreader = [System.Formats.Tar.TarReader]::new($gzipstream)
    while($entry = $tarreader.GetNextEntry()){
        if($entry.Name.EndsWith($BinaryName)){
            $entry.ExtractToFile($savepath, $true)
            break
        }
    }
    $tarreader.Dispose()
    $gzipstream.Dispose()
    $filestream.Dispose()
}
Remove-Item -Path $downloadfilename -Force
@"
Usage:
  aicm [--config PATH] [--profile NAME] [--region NAME]
  aicm daemon run [--config PATH]
  aicm daemon status
  aicm daemon stop
  aicm connections [--json]
  aicm close <connection-id> | --all
  aicm version
"@