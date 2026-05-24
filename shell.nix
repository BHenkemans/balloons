{ pkgs ? import <nixpkgs> {} }:

pkgs.mkShell {
  packages = with pkgs; [
    # Go
    go
    gopls
    gotools

    # Task runner
    just

    # Protobuf / connectRPC codegen
    buf
    protoc-gen-go
    protoc-gen-connect-go

    # Frontend build (esbuild via npm)
    nodejs_22

    # Ticket rendering (printer subsystem)
    typst
  ];

  shellHook = ''
    if [ -z "$IN_NIX_ZSH" ] && [ -x "$(command -v zsh)" ]; then
      export IN_NIX_ZSH=1
      exec zsh
    fi
  '';
}
