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

// Additional structural check for data.json
const dataPath = path.join(__dirname, 'data.json');
if (fs.existsSync(dataPath)) {
  try {
    const data = JSON.parse(fs.readFileSync(dataPath, 'utf-8'));
    if (!Array.isArray(data.councils) || data.councils.length === 0) {
      console.error('[verify-build] STRUCTURAL: data.json has no councils');
      failed++;
    } else {
      const firstWithDelibs = data.councils.find(c => Array.isArray(c.deliberations) && c.deliberations.length > 0);
      if (!firstWithDelibs) {
        console.error('[verify-build] STRUCTURAL: no council has any deliberations');
        failed++;
      } else {
        console.log(`[verify-build] OK: data.json structure valid (${data.councils.length} councils)`);
      }
    }
  } catch (e) {
    console.error(`[verify-build] PARSE FAILED: data.json — ${e.message}`);
    failed++;
  }
}

if (failed > 0) {
  console.error(`\n[verify-build] ${failed} file(s) missing or truncated — aborting deploy.`);
  process.exit(1);
}

console.log('\n[verify-build] All required assets present. Safe to deploy.');
