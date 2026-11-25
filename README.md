# music-bot-super-chat

A music bot for [Super Chat](https://github.com/canavan-a/super-chat), built
on its bot SDK (`github.com/canavan-a/super-chat/shared/bot`). Tag it in a chat:

| | |
|---|---|
| `@bot play <link or words>` | YouTube, SoundCloud and Spotify links (tracks, playlists, albums, sets), most links yt-dlp knows, direct audio links and radio streams, or words to search YouTube for |
| `@bot skip` | the next song |
| `@bot stop` | stop and clear the queue |
| `@bot pause` / `@bot resume` | |
| `@bot queue` | what's playing and what's next |
| `@bot exit` | stop the music and leave voice now |

It joins the chat's voice to play, keeps one queue per chat (in memory), and
leaves voice after 5 minutes with nobody else in it, or with nothing playing or
queued. The stock bot commands come too: `help`, `login`, `name`, `picture`,
`join` and `leave` (the last four for admins: `login <password>` first).

Spotify's audio is DRM-protected, so a Spotify track plays as YouTube's best
match for its artist and title, read from Spotify's public page.

## Running it

It needs `yt-dlp` and `ffmpeg` (with libopus); the flake bundles both.

```sh
nix run . -- init            # makes the account: asks for the admin password, prints the recovery phrase
nix run . -- run             # the bot
nix run . -- join <invite>   # from another shell: join a chat
```

Every command takes `--data DIR` (default `$SUPERCHAT_BOT_DATA`, else
`./bot-data`). `MUSIC_BOT_IDLE` changes the 5 minutes (e.g. `10m`).

On NixOS, use the module:

```nix
{
  inputs.music-bot.url = "github:canavan-a/music-bot-super-chat";
  # …
  imports = [ music-bot.nixosModules.default ];
  services.music-bot = {
    enable = true;
    passwordFile = "/run/secrets/music-bot-password";
  };
}
```

then `music-bot join <invite link>` on the box. yt-dlp has to keep up with the
sites it reads: update the flake's nixpkgs (`nix flake update`) if YouTube
stops working.

## Developing

```sh
nix develop
go test ./...
```
