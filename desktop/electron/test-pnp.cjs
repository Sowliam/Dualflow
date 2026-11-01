const { exec } = require('child_process');
const { promisify } = require('util');
const execAsync = promisify(exec);

async function test() {
  const [getmacRes, regRes] = await Promise.all([
    execAsync('getmac /fo csv /v', { windowsHide: true }),
    execAsync('reg query HKLM\\SYSTEM\\CurrentControlSet\\Control\\Class\\{4d36e972-e325-11ce-bfc1-08002be10318} /s', { windowsHide: true, maxBuffer: 10*1024*1024 })
  ]);

  const pnpMap = new Map();
  const blocks = regRes.stdout.split(/HKEY_LOCAL_MACHINE\\/i);
  for (const block of blocks) {
    const descMatch = block.match(/DriverDesc\s+REG_SZ\s+(.*)/i);
    const pnpMatch = block.match(/MatchingDeviceId\s+REG_SZ\s+(.*)/i);
    const mfgMatch = block.match(/ProviderName\s+REG_SZ\s+(.*)/i);
    const netCfgMatch = block.match(/NetCfgInstanceId\s+REG_SZ\s+(.*)/i);
    if (netCfgMatch) {
      const guid = netCfgMatch[1].trim().toLowerCase();
      pnpMap.set(guid, {
        desc: descMatch ? descMatch[1].trim() : '',
        pnpId: pnpMatch ? pnpMatch[1].trim() : '',
        mfg: mfgMatch ? mfgMatch[1].trim() : '',
      });
    }
  }

  const lines = getmacRes.stdout.trim().split(/\r?\n/).filter(Boolean);
  for (let i = 1; i < lines.length; i++) {
    const rawLine = lines[i];
    const parts = rawLine.slice(1, -1).split('","');
    const [name, desc, mac, transport] = parts;
    const guidMatch = transport.match(/\{[0-9A-Fa-f-]+\}/);
    const guid = guidMatch ? guidMatch[0].toLowerCase() : '';
    const hw = pnpMap.get(guid) || { desc: '', pnpId: '', mfg: '' };
    console.log(name, '-> PnP:', hw.pnpId, '| Mfg:', hw.mfg, '| Driver:', hw.desc || desc);
  }
}
test().catch(console.error);

