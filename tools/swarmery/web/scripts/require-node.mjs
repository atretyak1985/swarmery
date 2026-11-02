// Lingui 6's CLI runs a subcommand only `if (import.meta.main)`. Node before
// 22.18 on the 22 line, and before 24.2 on the 24 line, leaves that undefined,
// and Lingui 6 itself states 22.19 as its floor (package.json "engines"),
// so `lingui compile` and `lingui extract` exit 0 having done nothing: the build
// then ships whatever compiled catalogs are left on disk, and a message whose id
// changed format since (Lingui 6 ids are URL-safe) falls back to English.
// Fail before that happens.
if (import.meta.main !== true) {
  console.error(
    `Node ${process.version} cannot run the Lingui 6 CLI (it needs import.meta.main): ` +
      'use Node 22.19+ on the 22 line or 24.2+.',
  );
  process.exit(1);
}
