// The one table of file extensions of the Files page, shared by the web
// app (the type icon, web/src/filekind.ts) and the server (the options of
// the "Ekstenzija" filter chip, server/src/collections.ts).

/** Kind → the extensions (lower case) that have it. Folders and "other" have none. */
export const EXTENSIONS = {
  // heic and svg have no thumbnail (shfd thumbs does not draw them), but they are images.
  image: ["jpg", "jpeg", "png", "gif", "webp", "bmp", "tif", "tiff", "heic", "heif", "avif", "svg", "ico"],
  pdf: ["pdf"],
  word: ["doc", "docx", "odt", "rtf"],
  excel: ["xls", "xlsx", "ods", "csv"],
  powerpoint: ["ppt", "pptx", "odp"],
  text: ["txt", "md", "log"],
  code: ["html", "htm", "css", "js", "ts", "jsx", "tsx", "json", "xml", "yml", "yaml", "go", "py", "sh", "sql"],
  archive: ["zip", "tar", "gz", "tgz", "7z", "rar", "bz2", "xz"],
  audio: ["mp3", "wav", "flac", "ogg", "oga", "m4a", "aac", "opus"],
  video: ["mp4", "mov", "mkv", "avi", "webm", "m4v", "wmv"],
} as const satisfies Record<string, readonly string[]>;

/** Every extension of the table, sorted: what the extension chip offers. */
export function knownExtensions(): string[] {
  return Object.values(EXTENSIONS)
    .flat()
    .sort((a, b) => a.localeCompare(b));
}
