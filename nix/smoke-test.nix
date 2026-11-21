{
  lib,
  stdenv,
  go,
  cliproxyapi,
  magpie-patched,
  cpa-reductant,
  pluginPkg ? cpa-reductant,
  name ? "${cpa-reductant.pname}-smoke-test-${cpa-reductant.version}",
}:
stdenv.mkDerivation {
  inherit name;
  inherit (cpa-reductant) src;

  nativeBuildInputs = [
    go
    cliproxyapi
  ];

  postPatch = ''
    mkdir -p .cache
    ln -s ${magpie-patched} .cache/magpie
  '';

  configurePhase = ''
    runHook preConfigure
    export GOCACHE="$TMPDIR/go-cache"
    export GOPATH="$TMPDIR/go"
    export GOPROXY="file://${cpa-reductant.goModules}"
    export GOSUMDB=off
    runHook postConfigure
  '';

  doCheck = true;

  checkPhase = ''
    runHook preCheck
    CPA_BIN=${lib.getExe cliproxyapi} \
    CPA_PLUGIN_PATH=${pluginPkg}/lib/cpa-reductant${stdenv.hostPlatform.extensions.sharedLibrary} \
    go test -v ./test/...
    runHook postCheck
  '';

  installPhase = ''
    runHook preInstall
    touch "$out"
    runHook postInstall
  '';
}
