#!/usr/bin/env node
import fs from "node:fs";
import path from "node:path";

const outputDir = process.argv[2];
if (!outputDir) {
  console.error("Usage: collect-npm-notices.mjs OUTPUT_DIR");
  process.exit(2);
}

const root = process.cwd();
const lock = JSON.parse(fs.readFileSync(path.join(root, "package-lock.json"), "utf8"));
const packages = lock.packages ?? {};
fs.mkdirSync(outputDir, { recursive: true });
const index = [];
const missing = [];

function licenseFile(name) {
  return /^(?:license|licence|copying|notice|copyright)(?:$|[._-])/i.test(name);
}

for (const [lockPath, entry] of Object.entries(packages)) {
  if (!lockPath.startsWith("node_modules/")) continue;
  const packageName = lockPath.slice(lockPath.lastIndexOf("node_modules/") + "node_modules/".length);
  const packageDir = path.join(root, lockPath);
  let realDir;
  try {
    const stat = fs.lstatSync(packageDir);
    if (stat.isSymbolicLink() || !stat.isDirectory()) continue;
    realDir = packageDir;
  } catch {
    continue;
  }
  const version = entry.version ?? "";
  let declaredLicense = entry.license ?? null;
  try {
    const manifest = JSON.parse(fs.readFileSync(path.join(realDir, "package.json"), "utf8"));
    declaredLicense ??= manifest.license ?? manifest.licenses ?? null;
  } catch {
    // A package without a readable manifest is still indexed from the lockfile.
  }
  const record = { name: packageName, path: lockPath, version, license: declaredLicense, license_files: [] };
  const destination = path.join(outputDir, "packages", lockPath.slice("node_modules/".length));
  const stack = [realDir];
  while (stack.length) {
    const current = stack.pop();
    for (const item of fs.readdirSync(current, { withFileTypes: true })) {
      if (item.name === "node_modules") continue;
      const source = path.join(current, item.name);
      if (item.isSymbolicLink()) continue;
      if (item.isDirectory()) {
        stack.push(source);
      } else if (item.isFile() && licenseFile(item.name)) {
        const relative = path.relative(realDir, source);
        const target = path.join(destination, relative);
        fs.mkdirSync(path.dirname(target), { recursive: true });
        fs.copyFileSync(source, target);
        record.license_files.push(relative);
      }
    }
  }
  record.license_files.sort();
  if (record.license_files.length === 0) missing.push(`${packageName}\t${version}`);
  index.push(record);
}

index.sort((a, b) => a.name.localeCompare(b.name));
fs.writeFileSync(path.join(outputDir, "index.json"), `${JSON.stringify(index, null, 2)}\n`);
fs.writeFileSync(path.join(outputDir, "MISSING.txt"), missing.length ? `${missing.join("\n")}\n` : "");
