#!/usr/bin/env node
// Licence checker for incorporated third-party code (task P0-31).
//
// Reads the actual LICENSE/COPYING/NOTICE files of every Go module linked into
// the configured builds (from the local module cache) and of every npm package
// installed from the lockfiles, classifies them by their text, and compares the
// result with .licenses/allowlist.json. No network access; run `go mod download`
// and `npm ci` first.
//
// Usage (from the repository root, or via .licenses/check.sh):
//   node .licenses/check.mjs              check; exit 1 on a violation
//   node .licenses/check.mjs --report     markdown audit table on stdout
//   node .licenses/check.mjs --notices    THIRD_PARTY_NOTICES.md content on stdout
//   node .licenses/check.mjs --all        also audit the whole Go module graph
//                                         (go list -m all) and npm dev deps
import { execFileSync } from "node:child_process";
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const here = path.dirname(fileURLToPath(import.meta.url));
const root = path.resolve(here, "..");
const cfg = JSON.parse(fs.readFileSync(path.join(here, "allowlist.json"), "utf8"));
const args = new Set(process.argv.slice(2));
const mode = args.has("--notices") ? "notices" : args.has("--report") ? "report" : "check";
const all = args.has("--all");

const LICENCE_FILE = /^(licen[cs]e|copying|unlicense|notice|patents)([-._].*)?$/i;
const NOTICE_BUNDLE = /(3rd|third)[-_ ]?party/i;

// classify returns the SPDX ids whose canonical wording appears in text.
export function classify(text) {
  const t = text.replace(/[\s>*#|]+/g, " ");
  const ids = new Set();
  for (const m of text.matchAll(/SPDX-License-Identifier:\s*([A-Za-z0-9.+-]+(?:\s+(?:OR|AND|WITH)\s+[A-Za-z0-9.+-]+)*)/g)) {
    for (const id of m[1].split(/\s+(?:OR|AND|WITH)\s+/)) ids.add(id);
  }
  if (/Apache License,? Version 2\.0/i.test(t)) ids.add("Apache-2.0");
  if (/Permission is hereby granted, free of charge, to any person obtaining a copy/i.test(t)) ids.add("MIT");
  if (/Redistribution and use in source and binary forms/i.test(t)) {
    if (/advertising materials/i.test(t)) ids.add("BSD-4-Clause");
    else if (/(Neither the name|names? of (its|the) contributors may|name of the copyright holder nor|may not be used to endorse)/i.test(t)) ids.add("BSD-3-Clause");
    else ids.add("BSD-2-Clause");
  }
  if (/Permission to use, copy, modify, (and\/or|and) distribute this software for any purpose with or without fee/i.test(t)) {
    ids.add(/provided that the above copyright notice and this permission notice appear in all copies/i.test(t) ? "ISC" : "0BSD");
  }
  if (/This is free and unencumbered software released into the public domain/i.test(t)) ids.add("Unlicense");
  if (/CC0 1\.0 Universal/i.test(t)) ids.add("CC0-1.0");
  if (/provided ['‘]as-is['’].*Altered source versions must be plainly marked/i.test(t)) ids.add("Zlib");
  if (/Mozilla Public License/i.test(t)) ids.add("MPL-2.0");
  if (/Blue Oak Model License/i.test(t)) ids.add("BlueOak-1.0.0");
  if (/^ ?Attribution 4\.0 International/i.test(t) || /Creative Commons Attribution 4\.0/i.test(t)) ids.add("CC-BY-4.0");
  // MPL-2.0 names the GPL family as "Secondary Licenses"; do not count those mentions.
  if (ids.has("MPL-2.0")) {
    // no GPL-family detection inside an MPL text
  } else if (/GNU LESSER GENERAL PUBLIC LICENSE/i.test(t)) ids.add("LGPL");
  else if (/GNU AFFERO GENERAL PUBLIC LICENSE/i.test(t)) ids.add("AGPL");
  else if (/GNU GENERAL PUBLIC LICENSE/i.test(t)) ids.add("GPL");
  if (/Server Side Public License/i.test(t)) ids.add("SSPL");
  if (/Creative Commons Attribution/i.test(t) && /NonCommercial/i.test(t)) ids.add("CC-BY-NC");
  if (/Additional IP Rights Grant \(Patents\)/i.test(t)) ids.add("LicenseRef-Go-Patents");
  if (ids.size === 0 && /(dedicated|dedicate|released) (\w+ )?(to|into) the public domain|is public domain/i.test(t)) ids.add("LicenseRef-Public-Domain");
  return [...ids];
}

function copyrights(text) {
  const out = [];
  for (const line of text.split(/\r?\n/)) {
    const l = line.trim().replace(/^[*#>\s-]+/, "");
    if (/^(copyright\s*(\(c\)|©|\d|[A-Z])|©)/i.test(l) && !/^copyright (notice|holders?|owner|law|licen[cs]e|statement|and)|\[yyyy\]|\{yyyy\}/i.test(l) && l.length < 160) {
      if (!out.includes(l)) out.push(l);
    }
    if (out.length >= 4) break;
  }
  return out;
}

function licenceFiles(dir) {
  try {
    return fs.readdirSync(dir).filter((f) => LICENCE_FILE.test(f) && fs.statSync(path.join(dir, f)).isFile()).sort();
  } catch {
    return [];
  }
}

// nestedLicenceFiles finds licence files below dir that cover a linked package dir.
function nestedLicenceFiles(dir, pkgDirs) {
  const found = [];
  const walk = (d, depth) => {
    if (depth > 8) return;
    for (const e of fs.readdirSync(d, { withFileTypes: true })) {
      if (!e.isDirectory() || /^(testdata|examples?|_.*|\..*|node_modules)$/.test(e.name)) continue;
      const sub = path.join(d, e.name);
      const files = licenceFiles(sub);
      if (files.length && pkgDirs.some((p) => (p + path.sep).startsWith(sub + path.sep))) {
        for (const f of files) found.push(path.relative(dir, path.join(sub, f)).split(path.sep).join("/"));
      }
      walk(sub, depth + 1);
    }
  };
  if (dir && fs.existsSync(dir)) walk(dir, 0);
  return found;
}

function goEnv(name) {
  return execFileSync("go", ["env", name], { encoding: "utf8" }).trim();
}

function collectGo() {
  const mods = new Map();
  const ignore = cfg.ignoreModulePrefixes || [];
  for (const b of cfg.go || []) {
    for (const tags of b.tags || [""]) {
      for (const goos of [].concat(b.goos || "")) {
        const env = { ...process.env, GOWORK: "off" };
        if (goos) env.GOOS = goos;
        const a = ["list", "-deps", "-f", "{{if .Module}}{{.Module.Path}}\t{{.Module.Version}}\t{{.Module.Dir}}\t{{.Dir}}{{end}}"];
        if (tags) a.push("-tags", tags);
        a.push("./...");
        const out = execFileSync("go", a, { cwd: path.join(root, b.module), env, encoding: "utf8" });
        for (const line of out.split(/\r?\n/).filter(Boolean)) {
          const [p, v, d, pkgDir] = line.split("\t");
          if (ignore.some((x) => p.startsWith(x))) continue;
          const key = `${p}@${v}`;
          if (!mods.has(key)) mods.set(key, { eco: "go", name: p, version: v, dir: d, pkgDirs: [], usedBy: new Set(), bundled: true });
          const m = mods.get(key);
          m.usedBy.add(b.module);
          if (!m.pkgDirs.includes(pkgDir)) m.pkgDirs.push(pkgDir);
        }
      }
    }
  }
  if (cfg.go && cfg.go.length) {
    const goroot = goEnv("GOROOT");
    // Version left generic so the generated notices do not change with the toolchain.
    mods.set("go-stdlib", { eco: "go", name: "Go standard library and runtime", version: "(build toolchain)", dir: goroot, pkgDirs: [], usedBy: new Set(cfg.go.map((b) => b.module)), bundled: true, onlyFiles: ["LICENSE", "PATENTS"] });
  }
  if (all) {
    const modcache = goEnv("GOMODCACHE");
    for (const b of cfg.go || []) {
      const out = execFileSync("go", ["list", "-m", "-f", "{{.Path}}\t{{.Version}}\t{{.Dir}}", "all"], { cwd: path.join(root, b.module), env: { ...process.env, GOWORK: "off" }, encoding: "utf8" });
      for (const line of out.split(/\r?\n/).filter(Boolean)) {
        const [p, v, dd] = line.split("\t");
        if (ignore.some((x) => p.startsWith(x)) || !v) continue;
        // go list -m leaves Dir empty for modules not needed by the build; use the cache path.
        const d = dd || path.join(modcache, `${p}@${v}`.replace(/[A-Z]/g, (c) => "!" + c.toLowerCase()));
        const key = `${p}@${v}`;
        if (!mods.has(key)) mods.set(key, { eco: "go", name: p, version: v, dir: d, pkgDirs: [], usedBy: new Set(), bundled: false });
        if (!mods.get(key).bundled) mods.get(key).usedBy.add(b.module + " (graph only)");
      }
    }
  }
  return [...mods.values()];
}

function collectNpm() {
  const pkgs = new Map();
  for (const n of cfg.npm || []) {
    const dir = path.join(root, n.dir);
    const lock = JSON.parse(fs.readFileSync(path.join(dir, "package-lock.json"), "utf8"));
    for (const [k, v] of Object.entries(lock.packages || {})) {
      if (!k || v.link) continue;
      const name = k.slice(k.lastIndexOf("node_modules/") + "node_modules/".length);
      const bundledDev = (n.bundledDev || []).includes(name);
      if (v.dev && !bundledDev && !all) continue;
      if (v.optional && !fs.existsSync(path.join(dir, k))) continue; // platform-specific binary not installed here
      const key = `${name}@${v.version}`;
      const notBundled = (cfg.notBundled || []).some((x) => (x.endsWith("/") ? name.startsWith(x) : name === x));
      if (!pkgs.has(key)) pkgs.set(key, { eco: "npm", name, version: v.version, dir: path.join(dir, k), declared: v.license, pkgDirs: [], usedBy: new Set(), bundled: (!v.dev || bundledDev) && !notBundled, dev: !!v.dev });
      pkgs.get(key).usedBy.add(n.dir + (v.dev ? (bundledDev ? " (build, output bundled)" : " (dev)") : ""));
    }
  }
  return [...pkgs.values()];
}

function analyse(e) {
  const excl = (cfg.excludeFiles || {})[e.name] || [];
  let files = e.onlyFiles ? e.onlyFiles.filter((f) => fs.existsSync(path.join(e.dir, f))) : licenceFiles(e.dir);
  files = files.filter((f) => !excl.includes(f));
  if (e.eco === "go" && e.bundled && e.name !== "Go standard library and runtime") {
    files = files.concat(nestedLicenceFiles(e.dir, e.pkgDirs).filter((f) => !excl.includes(f)));
  }
  e.files = files.map((f) => {
    const text = fs.readFileSync(path.join(e.dir, f), "utf8");
    return { name: f, text, ids: classify(text), bundle: NOTICE_BUNDLE.test(path.basename(f)) };
  });
  const primary = e.files.filter((f) => !f.bundle && !/^(notice|patents)/i.test(path.basename(f.name)));
  e.ids = [...new Set(primary.flatMap((f) => f.ids))];
  e.noticeIds = [...new Set(e.files.filter((f) => f.bundle).flatMap((f) => f.ids))];
  e.spdx = (cfg.spdx || {})[e.name] || e.ids.join(" AND ");
  e.copyright = [...new Set(primary.flatMap((f) => copyrights(f.text)))].slice(0, 4);
  e.hasNotice = e.files.some((f) => /^notice/i.test(path.basename(f.name)));
  const allowed = new Set([...(cfg.allowed || []), ...(((cfg.exceptions || {})[e.name] || {}).allow || [])]);
  const problems = [];
  if (!e.dir || !fs.existsSync(e.dir)) problems.push("not in module cache / node_modules");
  else if (!e.files.length && e.declared) {
    e.ids = [e.declared];
    e.metadataOnly = true;
    e.spdx = e.declared;
    if (e.bundled) problems.push(`no licence file (package.json declares ${e.declared})`);
  } else if (!e.files.length) problems.push("no licence file");
  else if (!e.ids.length) problems.push("unrecognised licence text");
  for (const id of e.ids) if (!allowed.has(id)) problems.push(`not allowed: ${id}`);
  for (const id of e.noticeIds) if (!allowed.has(id) && !(cfg.allowedInNoticeBundles || []).includes(id)) problems.push(`bundled notice mentions: ${id}`);
  e.problems = problems;
  e.review = (cfg.manualReview || {})[e.name];
  return e;
}

const entries = [...collectGo(), ...collectNpm()].map(analyse).sort((a, b) => (a.eco + a.name).localeCompare(b.eco + b.name));
const enforced = entries.filter((e) => e.bundled);

if (mode === "report") {
  console.log("| Eco | Package | Version | Licence (from file text) | Declared | Files read | Copyright | Incorporated in | Status |");
  console.log("|---|---|---|---|---|---|---|---|---|");
  for (const e of entries) {
    const st = e.problems.length ? `FLAG: ${e.problems.join("; ")}` : e.review ? `REVIEW: ${e.review}` : "ok";
    const cell = (s) => String(s ?? "").replace(/\|/g, "\\|");
    console.log(`| ${e.eco} | ${cell(e.name)} | ${cell(e.version)} | ${cell(e.spdx || "?")}${e.noticeIds.length ? " (bundled notices: " + e.noticeIds.join(", ") + ")" : ""} | ${cell(e.declared || "")} | ${cell(e.files.map((f) => f.name).join(", "))} | ${cell(e.copyright[0] || "")} | ${cell([...e.usedBy].join(", "))}${e.bundled ? "" : " (not incorporated)"} | ${cell(st)} |`);
  }
} else if (mode === "notices") {
  const out = [];
  out.push(`# Third-party notices`, "", `${cfg.name} incorporates the third-party components listed below. Each is used under its own licence, reproduced here in full together with its copyright and NOTICE lines. ${cfg.name} itself is licensed under the PolyForm Noncommercial License 1.0.0 (see \`LICENSE\`); that licence does not apply to these components.`, "");
  out.push(`This file is generated by \`.licenses/check.mjs --notices\` from the licence files of the pinned versions. Do not edit by hand.`, "");
  out.push("## Summary", "", "| Component | Version | Licence | Used in |", "|---|---|---|---|");
  for (const e of enforced) out.push(`| ${e.name} | ${e.version} | ${e.spdx || "?"} | ${[...e.usedBy].join(", ")} |`);
  out.push("");
  for (const e of enforced) {
    out.push(`## ${e.name} ${e.version}`, "", `Licence: ${e.spdx}${e.files.length > 1 ? " (full texts below)" : ""}`, "");
    if (e.review) out.push(`Note: ${e.review}`, "");
    for (const f of e.files) {
      out.push(`### ${f.name}`, "", "````text", f.text.replace(/\r\n/g, "\n").replace(/\s+$/, ""), "````", "");
    }
  }
  process.stdout.write(out.join("\n"));
} else {
  let bad = 0;
  for (const e of enforced) {
    if (e.problems.length) {
      bad++;
      console.error(`FAIL ${e.eco} ${e.name}@${e.version}: ${e.problems.join("; ")}`);
    }
  }
  for (const e of entries.filter((x) => !x.bundled && x.problems.length)) console.error(`warn ${e.eco} ${e.name}@${e.version} (not incorporated): ${e.problems.join("; ")}`);
  console.log(`${enforced.length} incorporated components checked, ${bad} violation(s).`);
  process.exit(bad ? 1 : 0);
}
