// Tests csvField from packaging/www/app.js (U2: no formula injection).
'use strict';
const fs = require('fs');
const src = fs.readFileSync(__dirname + '/../packaging/www/app.js', 'utf8');
const m = src.match(/function csvField\(v, text\) \{[\s\S]*?\n\}/);
if (!m) { console.error('csvField not found'); process.exit(1); }
const csvField = new Function(m[0] + '; return csvField;')();
const cases = [
  ['=1+1', true, "'=1+1"],
  ['+SUM(A1:A2)', true, "'+SUM(A1:A2)"],
  ['-2+3', true, "'-2+3"],
  ['@cmd', true, "'@cmd"],
  ['\tx', true, "'\tx"],
  ['\rx', true, "\"'\rx\""],
  ['a;b', true, '"a;b"'],
  ['=HYPERLINK("x")', true, "\"'=HYPERLINK(\"\"x\"\")\""],
  ['Bad Thermostat', true, 'Bad Thermostat'],
  [-1, false, '-1'],
  ['-0,5', false, '-0,5'],
];
let bad = 0;
for (const [v, t, want] of cases) {
  const got = csvField(v, t);
  if (got !== want) { bad++; console.log('FAIL', JSON.stringify(v), JSON.stringify(got), 'want', JSON.stringify(want)); }
}
console.log(bad ? 'FAIL' : 'PASS ' + cases.length);
process.exit(bad ? 1 : 0);
