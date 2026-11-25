{ lib, buildGoModule, makeWrapper, ffmpeg-headless, yt-dlp }:

buildGoModule {
  pname = "music-bot";
  version = "0.1.0";
  src = lib.cleanSource ./.;

  # Run `nix build` after changing go.mod/go.sum: it fails with the hash to put here.
  vendorHash = lib.fakeHash;

  env.CGO_ENABLED = 0;
  ldflags = [ "-s" "-w" ];
  nativeBuildInputs = [ makeWrapper ];

  # The bot runs yt-dlp and ffmpeg for every song: ship them with it.
  postInstall = ''
    mv $out/bin/music-bot-super-chat $out/bin/music-bot
    wrapProgram $out/bin/music-bot \
      --prefix PATH : ${lib.makeBinPath [ ffmpeg-headless yt-dlp ]}
  '';

  meta = {
    description = "Super Chat music bot: YouTube, SoundCloud and Spotify in voice";
    license = lib.licenses.mit;
    mainProgram = "music-bot";
  };
}
