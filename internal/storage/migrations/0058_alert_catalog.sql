-- Filter/sort narrow metadata before fetching immutable evidence for page rows.
CREATE VIEW public.almanac_alert_catalog AS
SELECT o.id,o.policy_id,o.revision,o.state,o.opened_at,
 p.name AS policy_name,p.severity,r.slo_id,s.display_name AS slo_name,
 d.service_id,service.display_name AS service_name
FROM public.almanac_alert_occurrences o
JOIN public.almanac_alert_policies p ON p.id=o.policy_id
JOIN public.almanac_alert_policy_revisions r ON (r.policy_id,r.revision)=(o.policy_id,o.revision)
JOIN public.almanac_slos s ON s.id=r.slo_id
JOIN public.almanac_slo_definitions d ON (d.slo_id,d.revision)=(r.slo_id,r.definition_revision)
JOIN public.almanac_services service ON service.id=d.service_id;
