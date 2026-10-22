-- Version 5: who set a seat's label and colour. desktop.md C10.

ALTER TABLE seat ADD COLUMN look_src TEXT CHECK (look_src IS NULL OR look_src IN ('ui', 'slot', 'import'));

-- Backfill. Up to v4 only the prototype importer (device dev_import) wrote seat.label and
-- seat.colour (store.SeatLook), so on a hub that ran the importer a labelled seat came from
-- it; any other labelled seat is treated as a slot_info label, which a UI label overrides.
UPDATE seat SET look_src = 'import'
 WHERE (label IS NOT NULL OR colour IS NOT NULL)
   AND EXISTS (SELECT 1 FROM device WHERE id = 'dev_import');
UPDATE seat SET look_src = 'slot'
 WHERE look_src IS NULL AND label IS NOT NULL;
