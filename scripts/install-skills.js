#!/usr/bin/env node
'use strict';

const fs = require('fs');
const path = require('path');

const PKG_ROOT = path.resolve(__dirname, '..');
const TARGET = process.cwd();

if (TARGET === PKG_ROOT) {
  console.log('Skills are already present in this directory (running from the Reconify repo itself).');
  process.exit(0);
}

const SKILL_DIRS = [
  { source: 'skills/.agents', target: '.agents/skills' },
  { source: 'skills/.claude', target: '.claude/skills' },
  { source: 'skills/.codex', target: '.codex/skills' },
];

// Optional Claude Code hooks, installed with --hooks.
const HOOKS_SOURCE = 'skills/.hooks/claude';

const args = process.argv.slice(2);
const wantHooks = args.includes('--hooks');
const unknown = args.filter((arg) => arg !== '--hooks');
if (unknown.length > 0) {
  console.error(`Error: unknown argument(s): ${unknown.join(' ')}`);
  console.error('Usage: reconify-skills [--hooks]');
  process.exit(2);
}

function copyDir(src, dst) {
  fs.mkdirSync(dst, { recursive: true });
  for (const entry of fs.readdirSync(src, { withFileTypes: true })) {
    const srcPath = path.join(src, entry.name);
    const dstPath = path.join(dst, entry.name);
    if (entry.isDirectory()) {
      copyDir(srcPath, dstPath);
    } else {
      fs.copyFileSync(srcPath, dstPath);
      console.log(`  wrote ${path.relative(TARGET, dstPath)}`);
    }
  }
}

// Copy hook scripts into <target>/.claude/hooks/ and keep them executable.
function installHookScripts(srcDir, dstDir) {
  fs.mkdirSync(dstDir, { recursive: true });
  for (const entry of fs.readdirSync(srcDir, { withFileTypes: true })) {
    if (!entry.isFile()) continue;
    const dstPath = path.join(dstDir, entry.name);
    fs.copyFileSync(path.join(srcDir, entry.name), dstPath);
    fs.chmodSync(dstPath, 0o755);
    console.log(`  wrote ${path.relative(TARGET, dstPath)}`);
  }
}

function commandsOf(group) {
  return Array.isArray(group && group.hooks) ? group.hooks.map((hook) => hook && hook.command) : [];
}

// Merge hook entries from `incoming` into `existing` without removing or
// altering anything already there. Returns the number of hooks added.
function mergeHooks(existing, incoming) {
  let added = 0;
  if (existing.hooks === undefined) existing.hooks = {};
  if (typeof existing.hooks !== 'object' || existing.hooks === null || Array.isArray(existing.hooks)) {
    throw new Error('"hooks" in the existing settings.json is not an object');
  }
  for (const [event, incomingGroups] of Object.entries(incoming.hooks || {})) {
    if (existing.hooks[event] === undefined) existing.hooks[event] = [];
    const groups = existing.hooks[event];
    if (!Array.isArray(groups)) {
      throw new Error(`hooks.${event} in the existing settings.json is not an array`);
    }
    const present = new Set(groups.flatMap(commandsOf));
    for (const incomingGroup of incomingGroups) {
      const fresh = (incomingGroup.hooks || []).filter((hook) => !present.has(hook.command));
      if (fresh.length === 0) continue;
      const home = groups.find(
        (group) => group && Array.isArray(group.hooks) && group.matcher === incomingGroup.matcher,
      );
      if (home) {
        home.hooks.push(...fresh);
      } else {
        groups.push({ ...incomingGroup, hooks: fresh });
      }
      fresh.forEach((hook) => present.add(hook.command));
      added += fresh.length;
    }
  }
  return added;
}

function installHookSettings(srcFile, dstFile) {
  const incoming = JSON.parse(fs.readFileSync(srcFile, 'utf8'));
  let existing = {};
  if (fs.existsSync(dstFile)) {
    try {
      existing = JSON.parse(fs.readFileSync(dstFile, 'utf8'));
    } catch (err) {
      throw new Error(`${path.relative(TARGET, dstFile)} is not valid JSON (${err.message}); merge ${srcFile} by hand`);
    }
    if (typeof existing !== 'object' || existing === null || Array.isArray(existing)) {
      throw new Error(`${path.relative(TARGET, dstFile)} is not a JSON object; merge ${srcFile} by hand`);
    }
  }
  const added = mergeHooks(existing, incoming);
  if (added === 0) {
    console.log(`  ${path.relative(TARGET, dstFile)} already has the Reconify hooks`);
    return;
  }
  fs.mkdirSync(path.dirname(dstFile), { recursive: true });
  fs.writeFileSync(dstFile, `${JSON.stringify(existing, null, 2)}\n`);
  console.log(`  merged ${added} hook(s) into ${path.relative(TARGET, dstFile)}`);
}

console.log('Installing Reconify agent skills...\n');

let installed = 0;
for (const dir of SKILL_DIRS) {
  const src = path.join(PKG_ROOT, dir.source);
  if (!fs.existsSync(src)) continue;
  const dst = path.join(TARGET, dir.target);
  copyDir(src, dst);
  installed++;
}

if (installed === 0) {
  console.error('Error: skill source files not found in package. Re-install the package and try again.');
  process.exit(1);
}

if (wantHooks) {
  const hooksSrc = path.join(PKG_ROOT, HOOKS_SOURCE);
  if (!fs.existsSync(hooksSrc)) {
    console.error('Error: hook files not found in package. Re-install the package and try again.');
    process.exit(1);
  }
  console.log('\nInstalling Reconify Claude Code hooks...\n');
  try {
    // Settings first: an unreadable settings.json aborts before anything else is written.
    installHookSettings(path.join(hooksSrc, 'settings.json'), path.join(TARGET, '.claude', 'settings.json'));
    installHookScripts(path.join(hooksSrc, 'hooks'), path.join(TARGET, '.claude', 'hooks'));
  } catch (err) {
    console.error(`Error: ${err.message}`);
    process.exit(1);
  }
}

console.log(`\nDone. Skills installed into ${SKILL_DIRS.map((dir) => dir.target).join(', ')}.`);
if (wantHooks) {
  console.log('Hooks installed into .claude/hooks and .claude/settings.json. Set RECONIFY_HOOKS=off to disable them.');
} else {
  console.log('Optional: rerun with --hooks to add Claude Code hooks that validate reconify.yaml and run `reconify verify`.');
}
console.log('Run `go run ./cmd/reconify config schema` to verify the CLI is working.');
