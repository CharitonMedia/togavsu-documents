-- Published for other modules (the signing module reads it to find generated fee
-- agreements) under views:documents.generated_documents:read.
CREATE VIEW generated_documents AS
  SELECT g.file_id, g.template_id, t.name AS template_name, g.run_id, g.template_version_number
  FROM generated_files g LEFT JOIN templates t ON t.id = g.template_id;
