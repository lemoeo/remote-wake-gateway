import assert from 'node:assert/strict'
import { createHash } from 'node:crypto'
import { execFileSync } from 'node:child_process'
import fs from 'node:fs'
import { tmpdir } from 'node:os'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const tag = process.argv[2]
assert.match(tag || '', /^v\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?$/, 'Usage: node scripts/verify-release.mjs v0.1.0')
const version = tag.slice(1)
const project = fileURLToPath(new URL('../', import.meta.url))
const directory = path.join(project, 'dist', 'releases', tag)
const targets = ['linux-amd64', 'linux-arm64', 'linux-armv7', 'linux-mips', 'linux-mipsle', 'darwin-amd64', 'darwin-arm64', 'windows-amd64', 'windows-arm64']
const archives = targets.map(target => `remotewake-gateway_${tag}_${target.replaceAll('-', '_')}.${target.startsWith('windows-') ? 'zip' : 'tar.gz'}`)
assert.deepEqual(fs.readdirSync(directory).sort(), [...archives, 'SHA256SUMS'].sort(), 'Release must contain exactly nine archives and SHA256SUMS')
const manifest = new Map()
for (const line of fs.readFileSync(path.join(directory, 'SHA256SUMS'), 'utf8').trim().split('\n')) {
  const match = /^([0-9a-f]{64})  ([A-Za-z0-9_.-]+)$/.exec(line)
  assert(match, `Invalid checksum line: ${line}`)
  assert(!manifest.has(match[2]), `Duplicate checksum: ${match[2]}`)
  manifest.set(match[2], match[1])
}
assert.equal(manifest.size, 9)
const temporary = fs.mkdtempSync(path.join(tmpdir(), 'remotewake-release-verify-'))
const run = (program, args, options = {}) => execFileSync(program, args, { maxBuffer: 64 * 1024 * 1024, ...options })

function verifyArchitecture(binary, target) {
  const [os, arch] = target.split('-')
  if (os === 'windows') {
    assert.equal(binary.subarray(0, 2).toString(), 'MZ')
    const pe = binary.readUInt32LE(0x3c)
    assert.equal(binary.subarray(pe, pe + 4).toString(), 'PE\0\0')
    assert.equal(binary.readUInt16LE(pe + 4), arch === 'amd64' ? 0x8664 : 0xaa64)
  } else if (os === 'darwin') {
    assert.equal(binary.readUInt32LE(0), 0xfeedfacf)
    assert.equal(binary.readUInt32LE(4), arch === 'amd64' ? 0x01000007 : 0x0100000c)
  } else {
    assert.equal(binary.subarray(0, 4).toString(), '\x7fELF')
    assert.equal(binary[4], ['armv7', 'mips', 'mipsle'].includes(arch) ? 1 : 2)
    assert.equal(binary[5], arch === 'mips' ? 2 : 1)
    const machine = arch === 'mips' ? binary.readUInt16BE(18) : binary.readUInt16LE(18)
    assert.equal(machine, { amd64: 62, arm64: 183, armv7: 40, mips: 8, mipsle: 8 }[arch])
  }
}

try {
  for (const [i, target] of targets.entries()) {
    const archive = archives[i]
    const fullPath = path.join(directory, archive)
    assert.equal(createHash('sha256').update(fs.readFileSync(fullPath)).digest('hex'), manifest.get(archive), `Checksum mismatch: ${archive}`)
    const root = archive.replace(/\.(tar\.gz|zip)$/, '')
    const windows = target.startsWith('windows-')
    const binaryName = `remotewake-gateway${windows ? '.exe' : ''}`
    const files = [`${root}/README.md`, `${root}/VERSION`, `${root}/bin/${binaryName}`]
    if (target.startsWith('linux-')) {
      files.push(`${root}/deployments/systemd/remotewake-gateway.service`, `${root}/deployments/openwrt/remotewake-gateway.init`, `${root}/deployments/openwrt/remotewake-gateway.config`)
    } else if (target.startsWith('darwin-')) {
      files.push(`${root}/deployments/macos/com.lemoeo.remotewake-gateway.plist`)
    }
    const allowed = new Set(files)
    for (const file of files) {
      let parent = path.posix.dirname(file)
      while (parent !== '.') {
        allowed.add(`${parent}/`)
        parent = path.posix.dirname(parent)
      }
    }
    const entries = run(windows ? 'unzip' : 'tar', windows ? ['-Z1', fullPath] : ['-tzf', fullPath], { encoding: 'utf8' }).trim().split('\n')
    assert.equal(new Set(entries).size, entries.length, `Duplicate archive entries: ${archive}`)
    for (const entry of entries) assert(allowed.has(entry), `Unexpected archive entry: ${entry}`)
    for (const file of files) assert(entries.includes(file), `Missing package file: ${file}`)
    const readEntry = name => run(windows ? 'unzip' : 'tar', windows ? ['-p', fullPath, name] : ['-xOzf', fullPath, name])
    assert.equal(readEntry(`${root}/VERSION`).toString().trim(), version)
    const readme = readEntry(`${root}/README.md`).toString()
    assert(readme.includes('9000') && readme.includes(`/blob/${tag}/README.md`), `Incomplete quickstart: ${archive}`)
    const binary = readEntry(`${root}/bin/${binaryName}`)
    verifyArchitecture(binary, target)
    const extracted = path.join(temporary, target + (windows ? '.exe' : ''))
    fs.writeFileSync(extracted, binary, { mode: 0o755 })
    const info = run('go', ['version', '-m', extracted], { encoding: 'utf8' })
    // Trimmed Go binaries need not retain linker flags in build metadata.
    // Check the linked string here; native jobs and the host smoke test below
    // verify that the public version command actually returns it.
    assert(binary.includes(Buffer.from(version)), `Missing embedded version: ${archive}`)
    if (target.endsWith('-mips') || target.endsWith('-mipsle')) assert(info.includes('GOMIPS=softfloat'))
    if (target.endsWith('-armv7')) assert(info.includes('GOARM=7'))
    const host = `${process.platform === 'win32' ? 'windows' : process.platform}-${process.arch === 'x64' ? 'amd64' : process.arch}`
    if (target === host) assert.equal(run(extracted, ['version'], { encoding: 'utf8' }).trim(), `RemoteWake Gateway ${version}`)
    console.log(`Verified ${archive}`)
  }
  console.log('Verified nine release packages, executable architectures, versions, service templates and SHA256SUMS.')
} finally {
  fs.rmSync(temporary, { recursive: true, force: true })
}
