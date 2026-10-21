// The app's entry (package.json "main"). The Android widgets' headless task starts this bundle
// with no screen mounted, so the widget task handler must be registered here, at module scope,
// and not in a route file that expo-router only evaluates when it renders (phone.md P11).
import 'expo-router/entry';

import { registerAndroidWidgets } from './widgets/android/task-handler';

registerAndroidWidgets();
