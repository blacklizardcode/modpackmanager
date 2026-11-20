{
  lib,
  buildGoModule,
  pkg-config,
  gobject-introspection,
  wrapGAppsHook4,
  gtk4,
  libadwaita,
  librsvg,
  adwaita-icon-theme,
  qt6,
  # Frontends to build. The launcher is always built; at runtime it starts the
  # one that fits the desktop (Qt on KDE, GTK elsewhere) and falls back to
  # whichever is installed.
  withGtk ? true,
  withQt ? true,
}:

assert lib.assertMsg (
  withGtk || withQt
) "modpackmanager: enable at least one of withGtk and withQt";

buildGoModule (finalAttrs: {
  pname = "modpackmanager";
  version = lib.fileContents ../internal/version/VERSION;

  # Only what the Go build needs, so edits to the README, CI or nix files don't
  # trigger a rebuild. go.sum is "maybeMissing" so the flake still evaluates
  # without it; postPatch below then stops the build with a clear message.
  src = lib.fileset.toSource {
    root = ../.;
    fileset = lib.fileset.unions [
      ../go.mod
      (lib.fileset.maybeMissing ../go.sum)
      ../cmd
      ../internal
      ../ui
      ../data
    ];
  };

  # Hash of the vendored Go modules. nix/update-vendor-hash.sh writes it to
  # nix/vendor-hash.txt; rerun it whenever go.mod or go.sum change. Until the
  # file exists the build stops at a hash mismatch that prints the right value.
  vendorHash =
    let
      hashFile = ./vendor-hash.txt;
    in
    if builtins.pathExists hashFile && lib.fileContents hashFile != "" then
      lib.fileContents hashFile
    else
      lib.fakeHash;

  # Say what is wrong instead of letting Go fail cryptically. This is a build
  # step rather than an evaluation error so that `nix develop` still works,
  # which is where you would run `go mod tidy`.
  postPatch = ''
    if [ ! -s go.sum ]; then
      echo "modpackmanager: go.sum is missing. Run 'go mod tidy', commit it, and 'git add' it (flakes only see tracked files)." >&2
      exit 1
    fi
  '';

  nativeBuildInputs = [
    pkg-config
  ]
  ++ lib.optionals withGtk [
    gobject-introspection
    wrapGAppsHook4
  ]
  ++ lib.optionals withQt [ qt6.wrapQtAppsHook ];

  buildInputs =
    lib.optionals withGtk [
      gtk4
      libadwaita
      librsvg # SVG badges in mod descriptions
      adwaita-icon-theme # symbolic icons on desktops without a GNOME icon theme
    ]
    ++ lib.optionals withQt [
      qt6.qtbase
      qt6.qtsvg # SVG badges in mod descriptions
      qt6.qtwayland # native Wayland windows
    ];

  # Build only the requested frontends. (excludedPackages, unlike subPackages,
  # keeps Go's default test discovery, so internal/... is tested too.)
  excludedPackages =
    lib.optionals (!withGtk) [
      "cmd/modpackmanager-gtk"
      "ui/gtk"
    ]
    ++ lib.optionals (!withQt) [
      "cmd/modpackmanager-qt"
      "ui/qt"
    ];

  ldflags = [
    "-s"
    "-w"
  ];

  # The miqt Qt bindings are one enormous Go package; a lower GC target keeps
  # the compiler's peak memory down at a small cost in speed.
  env = lib.optionalAttrs withQt { GOGC = "50"; };

  # Both wrapper hooks would wrap every binary in bin/. Wrap only the frontend
  # that needs each toolkit's environment (plugin paths, schemas, icons).
  dontWrapGApps = true;
  dontWrapQtApps = true;
  preFixup =
    lib.optionalString withGtk ''
      wrapGApp "$out/bin/modpackmanager-gtk"
    ''
    + lib.optionalString withQt ''
      wrapQtApp "$out/bin/modpackmanager-qt"
    '';

  postInstall = ''
    install -Dm644 data/io.github.blakka.ModpackManager.desktop \
      $out/share/applications/io.github.blakka.ModpackManager.desktop
    substituteInPlace $out/share/applications/io.github.blakka.ModpackManager.desktop \
      --replace-fail "Exec=modpackmanager" "Exec=$out/bin/modpackmanager"
    install -Dm644 data/modpackmanager-mime.xml \
      $out/share/mime/packages/modpackmanager.xml
  '';

  meta = {
    description = "Edit and export Minecraft modpacks from Modrinth and CurseForge";
    homepage = "https://github.com/blacklizardcode/modpackmanager";
    mainProgram = "modpackmanager";
    platforms = lib.platforms.linux;
    # license: set this once the project has a LICENSE file.
  };
})
