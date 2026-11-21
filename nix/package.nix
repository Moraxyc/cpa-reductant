{
  lib,
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

  vendorHash = "sha256-FeuVe4CbQyJerUgkoEUewNJm6h0UrvrFMQMNld+oR6I=";
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
      -o cpa-reductant.so ./cmd/plugin
    runHook postBuild
  '';

  installPhase = ''
    runHook preInstall
    install -Dm755 cpa-reductant.so $out/lib/cpa-reductant.so
    install -Dm644 cpa-reductant.h $out/include/cpa-reductant.h
    runHook postInstall
  '';

  meta = {
    description = "CLIProxyAPI reversible redaction plugin powered by Magpie";
    platforms = lib.platforms.linux;
  };
})
