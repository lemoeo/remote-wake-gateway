import test from 'node:test'
import assert from 'node:assert/strict'
import { spawnSync } from 'node:child_process'
import { fileURLToPath } from 'node:url'

const script = fileURLToPath(new URL('./release.sh', import.meta.url))
for (const version of ['v0.1.0', 'v12.34.56', 'v1.0.0-rc.1', 'v1.0.0-alpha-2', 'v1.0.0-0']) {
  test(`accepts release tag ${version}`, () => {
    const result = spawnSync('sh', [script, '--check-version', version], { encoding: 'utf8' })
    assert.equal(result.status, 0, result.stderr)
    assert.equal(result.stdout.trim(), version.slice(1))
  })
}
for (const version of ['', '0.1.0', 'v1.2', 'v01.2.3', 'v1.02.3', 'v1.2.03', 'v1.2.3-', 'v1.2.3-rc..1', 'v1.2.3-01', 'v1.2.3+build', 'v1.2.3/other', 'v1.2.3\nother', 'v1.2.3;echo bad', 'v1.2.3-$(echo bad)']) {
  test(`rejects malformed or unsafe tag ${JSON.stringify(version)}`, () => {
    const result = spawnSync('sh', [script, '--check-version', version], { encoding: 'utf8' })
    assert.notEqual(result.status, 0)
    assert.equal(result.stdout, '')
  })
}
for (const targets of [['windows-386'], ['linux-amd64', 'linux-amd64']]) {
  test(`rejects invalid target list before building: ${targets.join(' ')}`, () => {
    const result = spawnSync('sh', [script, 'v0.0.0-input-test', ...targets], { encoding: 'utf8' })
    assert.notEqual(result.status, 0)
    assert.equal(result.stdout, '')
  })
}
