const { spawn } = require('child_process')
const path = require('path')

const electronPath = path.join(__dirname, 'node_modules', 'electron', 'dist', 'electron.exe')
console.log('Spawning Electron via:', electronPath)
const env = { ...process.env }
delete env.ELECTRON_RUN_AS_NODE

const child = spawn(electronPath, [path.join(__dirname, 'electron', 'verify-phase5.cjs')], {
  stdio: 'inherit',
  env,
})

child.on('exit', (code) => {
  console.log('Electron process exited with code:', code)
  process.exit(code || 0)
})
