// Phone-maker battery guides (phone.md P6.7), matched on the lower-case manufacturer.

export interface MakerGuide {
  /** The table row ("samsung", …, "other"). */
  maker: string;
  text: string;
}

const GUIDES: { makers: string[]; text: string }[] = [
  { makers: ['samsung'], text: 'Settings → Battery → Background usage limits → Never sleeping apps → add Switchyard' },
  { makers: ['xiaomi', 'redmi', 'poco'], text: 'Settings → Apps → Switchyard → Autostart on; Battery saver → No restrictions' },
  { makers: ['oneplus', 'oppo', 'realme'], text: "Settings → Battery → Switchyard → Don't optimize; allow background activity" },
  { makers: ['huawei', 'honor'], text: 'Settings → Battery → App launch → Switchyard → Manage manually, all three on' },
];

export const OTHER_GUIDE = 'Settings → Apps → Switchyard → Battery → Unrestricted';

/** The guide for a manufacturer (Build.MANUFACTURER, any case); the generic one when unknown. */
export function makerGuide(manufacturer: string | null | undefined): MakerGuide {
  const m = (manufacturer ?? '').trim().toLowerCase();
  for (const g of GUIDES) {
    const hit = g.makers.find((x) => m.includes(x));
    if (hit) return { maker: g.makers[0]!, text: g.text };
  }
  return { maker: 'other', text: OTHER_GUIDE };
}

/** Is the maker in the table (the guide is then shown even with the exemption granted, P6.7). */
export function knownMaker(manufacturer: string | null | undefined): boolean {
  return makerGuide(manufacturer).maker !== 'other';
}
