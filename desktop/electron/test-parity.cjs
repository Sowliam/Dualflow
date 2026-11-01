const { GoCoreBridge } = require('./services/goCoreBridge');
const { NetworkDiscoveryService } = require('./services/networkDiscovery');

async function runParityTest() {
  console.log('=== DUALFLOW PARITY AUDIT: GO CORE vs TYPESCRIPT ===\n');

  // 1. Initialize TypeScript Discovery
  const tsEngine = new NetworkDiscoveryService();
  const t0 = Date.now();
  const tsAdapters = await tsEngine.getAdapters();
  const tsElapsed = Date.now() - t0;
  console.log(`[TypeScript Discovery] Completed in ${tsElapsed}ms. Enumerated ${tsAdapters.length} adapters.`);

  // 2. Initialize Go Core Daemon
  const goBridge = new GoCoreBridge();
  const t1 = Date.now();
  await goBridge.start();
  const goAdapters = await goBridge.getAdapters();
  const goElapsed = Date.now() - t1;
  console.log(`[Go Core Discovery] Completed in ${goElapsed}ms. Enumerated ${goAdapters.length} adapters.\n`);

  // 3. Parity Comparison
  console.log('--- ADAPTER-BY-ADAPTER PARITY VERIFICATION ---');
  let discrepancies = 0;

  const tsMap = new Map(tsAdapters.map(a => [a.name.toLowerCase(), a]));
  const goMap = new Map(goAdapters.map(a => [a.name.toLowerCase(), a]));

  for (const [name, goAd] of goMap.entries()) {
    const tsAd = tsMap.get(name);
    if (!tsAd) {
      console.warn(`[DISCREPANCY] Adapter [${goAd.name}] found in Go Core but missing in TypeScript!`);
      discrepancies++;
      continue;
    }

    const checks = [
      { field: 'Status', go: goAd.status, ts: tsAd.status },
      { field: 'IPv4', go: goAd.ipv4, ts: tsAd.ipv4 },
      { field: 'Gateway', go: goAd.gateway, ts: tsAd.gateway },
      { field: 'Metric', go: goAd.metric, ts: tsAd.metric },
      { field: 'Classification', go: goAd.classification, ts: tsAd.classification },
      { field: 'Confidence', go: goAd.classificationConfidence, ts: tsAd.classificationConfidence },
    ];

    let matches = true;
    for (const c of checks) {
      if (c.go !== c.ts) {
        console.warn(`  [MISMATCH in ${goAd.name}] Field ${c.field}: Go='${c.go}' vs TS='${c.ts}'`);
        matches = false;
        discrepancies++;
      }
    }

    if (matches) {
      console.log(`✓ Adapter [${goAd.name}]: 100% PARITY (IP: ${goAd.ipv4 || '—'}, GW: ${goAd.gateway || '—'}, Class: ${goAd.classification})`);
    }
  }

  console.log(`\nParity Audit Finished. Total Discrepancies: ${discrepancies}`);

  await goBridge.stop();
  process.exit(discrepancies === 0 ? 0 : 1);
}

runParityTest().catch((err) => {
  console.error('Fatal parity test error:', err);
  process.exit(1);
});

