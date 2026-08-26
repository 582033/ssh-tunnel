#!/bin/bash
# 由 assets/icon.svg 生成 icon.icns（.app 图标）和 libs/gui/appicon.png（Fyne 运行时图标）。
# 只在改了 icon.svg 之后需要跑，build.sh 直接用生成好的文件。
#
# 渲染方式几经周折，记录一下别再踩：
#   * rsvg-convert / cairosvg / PIL 在这台机器上都没有
#   * headless Chrome --screenshot 打开 SVG 会挂住（2 分钟没结果）
#   * qlmanage -t 能出图，但会把背景铺成不透明白色 —— 圆角外的四角变成白方块，
#     Dock 里就是一个白底方片，圆角全废
# 最后用 AppKit：NSImage 读 SVG（macOS 13+），画到一张透明位图上再存 PNG。
# 走 osascript -l JavaScript 而不是 swiftc，因为这台机器的 CommandLineTools
# modulemap 有重复定义，swiftc 编不过。
set -euo pipefail

cd "$(dirname "$0")"

WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT

cat > "$WORK/render.js" <<'JS'
ObjC.import('AppKit');
function run(argv) {
  var inPath = argv[0], outPath = argv[1], size = parseInt(argv[2], 10);
  var img = $.NSImage.alloc.initWithContentsOfFile($(inPath));
  var rep = $.NSBitmapImageRep.alloc
      .initWithBitmapDataPlanesPixelsWidePixelsHighBitsPerSampleSamplesPerPixelHasAlphaIsPlanarColorSpaceNameBytesPerRowBitsPerPixel(
          $(), size, size, 8, 4, true, false, $.NSDeviceRGBColorSpace, 0, 0);
  rep.setSize($.NSMakeSize(size, size));
  var ctx = $.NSGraphicsContext.graphicsContextWithBitmapImageRep(rep);
  $.NSGraphicsContext.saveGraphicsState;
  $.NSGraphicsContext.setCurrentContext(ctx);
  img.drawInRectFromRectOperationFraction(
      $.NSMakeRect(0, 0, size, size), $.NSMakeRect(0, 0, 0, 0),
      $.NSCompositeSourceOver, 1.0);
  ctx.flushGraphics;
  $.NSGraphicsContext.restoreGraphicsState;
  var data = rep.representationUsingTypeProperties($.NSPNGFileType, $());
  data.writeToFileAtomically($(outPath), true);
  return 'ok';
}
JS

SET="$WORK/icon.iconset"
mkdir -p "$SET"

# 每档尺寸单独渲染，而不是缩放一张大图：小尺寸下矢量重绘比重采样清楚得多
render() { # render <输出文件> <边长>
  osascript -l JavaScript "$WORK/render.js" \
    "$PWD/icon.svg" "$SET/$1" "$2" >/dev/null
  [ -s "$SET/$1" ] || { echo "渲染 $1 失败" >&2; exit 1; }
}

render icon_16x16.png 16
render icon_16x16@2x.png 32
render icon_32x32.png 32
render icon_32x32@2x.png 64
render icon_128x128.png 128
render icon_128x128@2x.png 256
render icon_256x256.png 256
render icon_256x256@2x.png 512
render icon_512x512.png 512
render icon_512x512@2x.png 1024

iconutil -c icns "$SET" -o icon.icns
# 同一张 512 PNG 内嵌进程序，供 Fyne 设置窗口/运行时图标
cp "$SET/icon_512x512.png" ../libs/gui/appicon.png

echo "已生成 assets/icon.icns 与 libs/gui/appicon.png"
