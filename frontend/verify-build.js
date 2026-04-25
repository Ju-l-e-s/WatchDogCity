const fs = require('fs');
const path = require('path');

const REQUIRED = [
  { name: 'index.html', minBytes: 1024 },
  { name: 'merci.html', minBytes: 256 },
  { name: 'app.js', minBytes: 1024 },
  { name: 'style.css', minBytes: 1024 },
  { name: 'data.json', minBytes: 32 },
];

let failed = 0;
for (const { name, minBytes } of REQUIRED) {
  const fp = path.join(__dirname, name);
  if (!fs.existsSync(fp)) {
    console.error(`[verify-build] MISSING: ${name}`);
    failed++;
    continue;
  }
  const size = fs.statSync(fp).size;
  if (size < minBytes) {
    console.error(`[verify-build] TRUNCATED: ${name} (${size}B < ${minBytes}B)`);
    failed++;
    continue;
  }
  console.log(`[verify-build] OK: ${name} (${size}B)`);
}

if (failed > 0) {
  console.error(`\n[verify-build] ${failed} file(s) missing or truncated — aborting deploy.`);
  process.exit(1);
}

console.log('\n[verify-build] All required assets present. Safe to deploy.');
