const { exec } = require('child_process');
const { promisify } = require('util');
const execAsync = promisify(exec);

async function testTelemetry() {
  console.log('=== DUALFLOW TELEMETRY VALIDATION TEST ===');

  // 1. Throughput Sample 1
  const t0 = Date.now();
  const sub1 = await execAsync('netsh interface ipv4 show subinterfaces', { windowsHide: true });
  console.log('Subinterfaces Sample 1 (t=0ms):');
  console.log(sub1.stdout);

  // Wait 1.5 seconds
  await new Promise(r => setTimeout(r, 1500));

  // 2. Throughput Sample 2
  const t1 = Date.now();
  const sub2 = await execAsync('netsh interface ipv4 show subinterfaces', { windowsHide: true });
  console.log('Subinterfaces Sample 2 (t=' + (t1 - t0) + 'ms):');
  console.log(sub2.stdout);

  // Parse lines
  const parseLines = (stdout) => {
    const lines = stdout.trim().split(/\r?\n/);
    const map = new Map();
    let isTable = false;
    for (const line of lines) {
      if (line.includes('---')) { isTable = true; continue; }
      if (!isTable) continue;
      const match = line.trim().match(/^(\d+)\s+(\d+)\s+(\d+)\s+(\d+)\s+(.+)$/);
      if (match) {
        map.set(match[5].trim(), {
          mtu: parseInt(match[1], 10),
          state: parseInt(match[2], 10),
          bytesIn: parseInt(match[3], 10),
          bytesOut: parseInt(match[4], 10),
        });
      }
    }
    return map;
  };

  const map1 = parseLines(sub1.stdout);
  const map2 = parseLines(sub2.stdout);
  const elapsedSec = (t1 - t0) / 1000;

  console.log('\n--- Real Throughput Calculations ---');
  for (const [name, row2] of map2.entries()) {
    const row1 = map1.get(name);
    if (!row1) continue;
    const deltaIn = row2.bytesIn - row1.bytesIn;
    const deltaOut = row2.bytesOut - row1.bytesOut;
    const rxMbps = (deltaIn * 8) / elapsedSec / 1000000;
    const txMbps = (deltaOut * 8) / elapsedSec / 1000000;
    const status = row2.state === 1 ? 'CONNECTED' : 'DISCONNECTED';
    console.log(`Interface: [${name}] | Status: ${status} | ΔRx: ${deltaIn} B | ΔTx: ${deltaOut} B | RxRate: ${rxMbps.toFixed(4)} Mbps | TxRate: ${txMbps.toFixed(4)} Mbps`);
  }

  // 3. Source-bound RTT probe test
  console.log('\n--- Source-Bound RTT Probe Test ---');
  const probeTargets = ['1.1.1.1', '8.8.8.8'];
  const sourceIps = ['192.168.1.10', '172.17.96.1']; // Real Ethernet vs Hyper-V

  for (const src of sourceIps) {
    for (const tgt of probeTargets) {
      const probeStart = Date.now();
      try {
        const cmd = `ping.exe -n 1 -w 1000 -S ${src} ${tgt}`;
        const res = await execAsync(cmd, { windowsHide: true });
        const timeMatch = res.stdout.match(/time[=<]([0-9]+)ms/i);
        const rtt = timeMatch ? parseInt(timeMatch[1], 10) : null;
        console.log(`Probe [${src} -> ${tgt}] | Result: SUCCESS | RTT: ${rtt} ms | Elapsed: ${Date.now() - probeStart} ms`);
      } catch (err) {
        console.log(`Probe [${src} -> ${tgt}] | Result: FAILED (Expected for unrouted virtual switch) | Error: ${err.message.split('\n')[0]}`);
      }
    }
  }
}

testTelemetry().catch(console.error);

