// The self-hosted profile's Android manifest edit (phone.md P13): see plugins/with-sy-native.js.
// Listed in app.config.ts BEFORE expo-notifications (manifest mods run in reverse order).
module.exports = require('./with-sy-native').withSyNoFirebase;
