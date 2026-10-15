#!/usr/bin/env node
'use strict';

// tscp 的 npm 启动器: 按当前平台选择预编译的 Go 二进制并拉起, 透传所有参数

const os = require('os');
const path = require('path');
const fs = require('fs');
const { spawn } = require('child_process');

const platformMap = { win32: 'windows', linux: 'linux', darwin: 'darwin', freebsd: 'freebsd' };
const archMap = { x64: 'amd64', arm64: 'arm64', ia32: '386' };

const platform = platformMap[process.platform];
const arch = archMap[os.arch()];

function fail(msg) {
  console.error('tscp: ' + msg);
  process.exit(1);
}

if (!platform || !arch) {
  fail(`不支持的平台 ${process.platform}-${os.arch()}`);
}

const binariesDir = path.join(__dirname, '..', 'binaries');
const binName = process.platform === 'win32' ? 'tscp.exe' : 'tscp';
const bin = path.join(binariesDir, `${platform}-${arch}`, binName);

if (!fs.existsSync(bin)) {
  let available = '';
  try {
    available = fs.readdirSync(binariesDir).join(', ');
  } catch (_) { /* ignore */ }
  fail(`没有 ${platform}-${arch} 的预编译版本(可用: ${available || '无'}), 请到源码目录 go build 自行编译`);
}

// npm 包在 Windows 上打包时不含可执行位, 首次运行前补上
try {
  const st = fs.statSync(bin);
  if ((st.mode & 0o111) === 0) {
    fs.chmodSync(bin, 0o755);
  }
} catch (_) { /* ignore */ }

const child = spawn(bin, process.argv.slice(2), { stdio: 'inherit', env: process.env });
child.on('error', (err) => fail(`启动失败: ${err.message}`));
child.on('close', (code) => process.exit(code == null ? 1 : code));
