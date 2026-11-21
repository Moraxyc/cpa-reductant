{
  lib,
  stdenv,
  buildGoModule,
  magpie-patched,
}:
buildGoModule (finalAttrs: {
  pname = "cpa-reductant";
  version = lib.trim (builtins.readFile ../VERSION);

  src = lib.fileset.toSource {
    root = ./..;
    fileset = lib.fileset.unions [
      ../go.mod
      ../go.sum
      ../cmd
      ../internal
      ../test
    ];
  };

  vendorHash = "sha256-lBx8VEjsyfbDdIRX0lMuffGUPJEdJSBl4oT/6w6LnKE=";
  proxyVendor = true;

  postPatch = ''
    mkdir -p .cache
    ln -s ${magpie-patched} .cache/magpie
  '';

  env.CGO_ENABLED = "1";

  # go install -buildmode=c-shared refuses main packages outside GOPATH without -o
  buildPhase = ''
    runHook preBuild
    go build -buildvcs=false -buildmode=c-shared \
      -ldflags "-s -w -X github.com/moraxyc/cpa-reductant/internal/config.Version=${finalAttrs.version}" \
      -o cpa-reductant${stdenv.hostPlatform.extensions.sharedLibrary} ./cmd/plugin
    runHook postBuild
  '';

  checkPhase = ''
    runHook preCheck
    export GOFLAGS=''${GOFLAGS//-trimpath/}
    go test -race -v ./internal/...
    runHook postCheck
  '';

  installPhase = ''
    runHook preInstall
    target="$out/lib/cliproxyapi/plugins/${stdenv.hostPlatform.go.GOOS}/${stdenv.hostPlatform.go.GOARCH}"
    install -Dm755 cpa-reductant${stdenv.hostPlatform.extensions.sharedLibrary} "$target/cpa-reductant${stdenv.hostPlatform.extensions.sharedLibrary}"
    install -Dm644 cpa-reductant.h "$out/include/cpa-reductant.h"
    runHook postInstall
  '';

  meta = {
    description = "CLIProxyAPI reversible redaction plugin powered by Magpie";
    license = lib.licenses.mit;
    platforms = lib.platforms.linux ++ lib.platforms.darwin;
  };
})
