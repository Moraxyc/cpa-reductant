{
  lib,
  stdenv,
  go,
  zig,
  magpie-patched,
  cpa-reductant,
  goos,
  goarch,
  zigTarget,
  ext,
}:
stdenv.mkDerivation {
  pname = "cpa-reductant-portable-${goos}-${goarch}";
  inherit (cpa-reductant) version src;

  nativeBuildInputs = [
    go
    zig
  ];

  postPatch = ''
    mkdir -p .cache
    ln -s ${magpie-patched} .cache/magpie
  '';

  configurePhase = ''
    runHook preConfigure
    export HOME="$TMPDIR"
    export GOCACHE="$TMPDIR/go-cache"
    export GOPATH="$TMPDIR/go"
    export GOPROXY="file://${cpa-reductant.goModules}"
    export GOSUMDB=off
    export ZIG_GLOBAL_CACHE_DIR="$TMPDIR/zig-global"
    export ZIG_LOCAL_CACHE_DIR="$TMPDIR/zig-local"
    runHook postConfigure
  '';

  buildPhase = ''
    runHook preBuild
    export CGO_ENABLED=1
    export GOOS=${lib.escapeShellArg goos}
    export GOARCH=${lib.escapeShellArg goarch}
    export GOAMD64=v1
    export GOARM64=v8.0
    export CC="zig cc -target ${zigTarget}"
    export CXX="zig c++ -target ${zigTarget}"
    go build -buildvcs=false -buildmode=c-shared -trimpath \
      -ldflags "-s -w -X github.com/moraxyc/cpa-reductant/internal/config.Version=${cpa-reductant.version}" \
      -o cpa-reductant${ext} ./cmd/plugin
    runHook postBuild
  '';

  installPhase = ''
    runHook preInstall
    target="$out/lib/cliproxyapi/plugins/${goos}/${goarch}"
    install -Dm755 cpa-reductant${ext} "$target/cpa-reductant${ext}"
    install -Dm644 cpa-reductant.h "$out/include/cpa-reductant.h"
    runHook postInstall
  '';

  dontStrip = true;
  dontPatchELF = true;

  meta = {
    description = "Portable ${goos}/${goarch} build of cpa-reductant";
    platforms = lib.platforms.linux;
  };
}
