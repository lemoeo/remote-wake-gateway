#!/bin/sh
set -eu
cd "$(dirname "$0")/.."
export LC_ALL=C
export COPYFILE_DISABLE=1
umask 022

die() { printf '%s\n' "$*" >&2; exit 1; }
validate_version() {
  case "$1" in
    v[0-9]* ) ;;
    * ) die '版本必须是 vMAJOR.MINOR.PATCH，可带 -rc.1 等预发布后缀' ;;
  esac
  release_version=${1#v}
  case "$release_version" in
    *[!A-Za-z0-9.-]* ) die '版本包含不支持的字符' ;;
  esac
  printf '%s\n' "$release_version" | awk '
    function number(s) { return s ~ /^[0-9]+$/ && (length(s) == 1 || substr(s, 1, 1) != "0") }
    {
      dash = index($0, "-")
      core = dash ? substr($0, 1, dash - 1) : $0
      if (split(core, v, ".") != 3 || !number(v[1]) || !number(v[2]) || !number(v[3])) exit 1
      if (dash) {
        suffix = substr($0, dash + 1)
        if (suffix == "") exit 1
        n = split(suffix, ids, ".")
        for (i = 1; i <= n; i++) {
          if (ids[i] !~ /^[0-9A-Za-z-]+$/) exit 1
          if (ids[i] ~ /^[0-9]+$/ && !number(ids[i])) exit 1
        }
      }
    }
  ' || die '版本格式无效，不允许缺少版本段、空预发布标识或数字前导零'
}

if [ "${1:-}" = --check-version ]; then
  [ "$#" -eq 2 ] || die '用法: release.sh --check-version v0.1.0'
  validate_version "$2"
  printf '%s\n' "$release_version"
  exit 0
fi

[ "$#" -ge 1 ] || die '用法: release.sh v0.1.0 [linux-amd64 ...]'
release_tag=$1
shift
validate_version "$release_tag"
if [ "$#" -eq 0 ]; then
  set -- linux-amd64 linux-arm64 linux-armv7 linux-mips linux-mipsle darwin-amd64 darwin-arm64 windows-amd64 windows-arm64
fi
seen=' '
for target in "$@"; do
  case "$target" in
    linux-amd64|linux-arm64|linux-armv7|linux-mips|linux-mipsle|darwin-amd64|darwin-arm64|windows-amd64|windows-arm64) ;;
    *) die "不支持的构建目标: $target" ;;
  esac
  case "$seen" in *" $target "*) die "重复的构建目标: $target" ;; esac
  seen="$seen$target "
done

for tool in go tar zip; do
  command -v "$tool" >/dev/null 2>&1 || die "缺少构建工具: $tool"
done
if command -v sha256sum >/dev/null 2>&1; then
  checksum() { sha256sum "$1"; }
elif command -v shasum >/dev/null 2>&1; then
  checksum() { shasum -a 256 "$1"; }
else
  die '需要 sha256sum 或 shasum'
fi

release_parent="$(pwd)/dist/releases"
release_out="$release_parent/$release_tag"
[ ! -e "$release_out" ] || die "输出目录已存在，拒绝覆盖: $release_out"
if [ "${RWG_SKIP_WEB:-0}" != 1 ]; then
  (cd web && npm ci --no-audit --no-fund && npm run build)
fi
[ -f internal/web/dist/index.html ] || die '缺少前端构建产物，请先构建前端'
mkdir -p "$release_parent"
release_work=$(mktemp -d "$release_parent/.build-XXXXXXXX")
trap 'rm -rf "$release_work"' 0
trap 'exit 1' HUP INT TERM
mkdir -p "$release_work/packages" "$release_work/assets"

for target in "$@"; do
  target_os=${target%%-*}
  target_arch=${target#*-}
  [ "$target_arch" != armv7 ] || target_arch=arm
  package_name="remotewake-gateway_${release_tag}_$(printf '%s' "$target" | tr '-' '_')"
  package_dir="$release_work/packages/$package_name"
  mkdir -p "$package_dir/bin"
  executable=remotewake-gateway
  [ "$target_os" != windows ] || executable="$executable.exe"
  printf 'Building %s (%s)\n' "$target" "$release_version"
  GOOS="$target_os" GOARCH="$target_arch" GOARM=7 GOMIPS=softfloat GOAMD64=v1 CGO_ENABLED=0 \
    go build -trimpath -ldflags="-s -w -X main.version=$release_version" \
    -o "$package_dir/bin/$executable" ./cmd/remotewake-gateway
  chmod 755 "$package_dir/bin/$executable"
  printf '%s\n' "$release_version" > "$package_dir/VERSION"
  {
    printf '# RemoteWake Gateway %s\n\n目标架构：`%s`。\n\n' "$release_tag" "$target"
    if [ "$target_os" = windows ]; then
      cat <<'WINDOWS'
在 PowerShell 中进入解压后的目录，运行：

```powershell
$RWG_DATA = Join-Path $env:LOCALAPPDATA 'RemoteWakeGateway\data'
& .\bin\remotewake-gateway.exe serve --data-dir $RWG_DATA
```

另开终端进入同一目录并重新设置 RWG_DATA，执行：

```powershell
$RWG_DATA = Join-Path $env:LOCALAPPDATA 'RemoteWakeGateway\data'
& .\bin\remotewake-gateway.exe token --data-dir $RWG_DATA
```

使用当前账户可写的本地 NTFS 数据目录，使用 Ctrl+C 停止程序。
WINDOWS
    else
      cat <<'UNIX'
在终端进入解压后的目录，运行：

```sh
./bin/remotewake-gateway serve --data-dir ./data
```

另开终端进入同一目录，读取首次初始化令牌：

```sh
./bin/remotewake-gateway token --data-dir ./data
```

后台服务模板位于 deployments；首次安装请按完整部署说明配置路径及账户。
UNIX
    fi
    cat <<'COMMON'

默认访问 http://127.0.0.1:9000，使用初始化令牌设置 6–72 字节管理员密码。
修改为 LAN / VPN 监听地址需显式配置，已有数据目录保留原监听地址。
升级或备份前停止程序，保留完整数据目录（含 secrets 和 jobs），不要删除 .lock 解锁。
COMMON
    [ "$target_arch" != mips ] && [ "$target_arch" != mipsle ] || printf '\n本 MIPS 构建使用 softfloat；请确认 CPU 大小端和固件兼容性。\n'
    printf '\n完整部署说明：https://github.com/lemoeo/remote-wake-gateway/blob/%s/README.md\n' "$release_tag"
  } > "$package_dir/README.md"
  case "$target_os" in
    linux)
      mkdir -p "$package_dir/deployments/systemd" "$package_dir/deployments/openwrt"
      cp deployments/systemd/remotewake-gateway.service "$package_dir/deployments/systemd/"
      cp deployments/openwrt/remotewake-gateway.init deployments/openwrt/remotewake-gateway.config "$package_dir/deployments/openwrt/"
      ;;
    darwin)
      mkdir -p "$package_dir/deployments/macos"
      cp deployments/macos/com.lemoeo.remotewake-gateway.plist "$package_dir/deployments/macos/"
      ;;
  esac
  if [ "$target_os" = windows ]; then
    (cd "$release_work/packages" && zip -q -r "$release_work/assets/$package_name.zip" "$package_name")
  else
    tar -C "$release_work/packages" -czf "$release_work/assets/$package_name.tar.gz" "$package_name"
  fi
done

(
  cd "$release_work/assets"
  for archive in *.tar.gz *.zip; do
    [ ! -f "$archive" ] || checksum "$archive"
  done > SHA256SUMS
)
# mkdir reserves the final destination atomically; concurrent builds never merge
# or overwrite an existing version's output.
mkdir "$release_out"
mv "$release_work/assets/"* "$release_out/"
printf 'Release assets: %s\n' "$release_out"
