const REQUIRED = [
  'APPLE_ID',
  'APPLE_APP_SPECIFIC_PASSWORD',
  'APPLE_TEAM_ID',
  'CSC_LINK',
  'CSC_KEY_PASSWORD',
  'AGENTICO_RELEASE_GPG_KEY',
  'AGENTICO_RELEASE_GPG_KEY_ID',
];

const missing = REQUIRED.filter((key) => (process.env[key] ?? '').trim() === '');
if (missing.length > 0) {
  console.error(`missing protected release credential inputs:\n- ${missing.join('\n- ')}`);
  process.exit(2);
}
console.log('protected release credential inputs are present');
