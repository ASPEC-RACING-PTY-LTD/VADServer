# FFmpeg in the VaultDock server image

The server image ships the `ffmpeg` and `ffprobe` programs (FFmpeg 7.1.1) in
`/usr/local/bin`, from the static builds published as
`mwader/static-ffmpeg:7.1.1` (build recipe: https://github.com/wader/static-ffmpeg).
VaultDock runs them as separate programs for video thumbnails and the video
editor's clip exports; it does not link to them.

FFmpeg is licensed under the GNU Lesser General Public License 2.1 or later;
these builds enable GPL components (such as libx264), so the programs as
shipped are covered by the GNU General Public License version 3 or later.
That licence covers FFmpeg only, not VaultDock (see `LICENSE`).

FFmpeg source code: https://ffmpeg.org/download.html (release 7.1.1), and the
exact sources and configuration of these builds through the recipe above.

Servers installed without the image can use their own FFmpeg: put `ffmpeg`
and `ffprobe` on the PATH or set `VAULTDOCK_FFMPEG` and `VAULTDOCK_FFPROBE`.
Without them, video thumbnails and the video editor are turned off.
