{
  description = "Shrooms — watch a mycelial mesh";

  inputs = {
    logos-module-builder.url = "github:logos-co/logos-module-builder";
  };

  # mkLogosQmlModule gives us `lgx` and `lgx-portable` package outputs from the
  # metadata.json next to this file. The portable one is what to install on a
  # machine that did not build it: the plain `lgx` can reference paths in the
  # building machine's nix store, which is fine for a dev loop and useless as a
  # download.
  #
  # A separate flake from the repo root on purpose. This packages the QML view
  # only — no Go, no cgo, no liblogosdelivery — and hoisting it to the root
  # would drag the whole daemon's build into a package that is four files.
  outputs = inputs@{ logos-module-builder, ... }:
    logos-module-builder.lib.mkLogosQmlModule {
      src = ./.;
      configFile = ./metadata.json;
      flakeInputs = inputs;
    };
}
