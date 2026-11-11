ALTER TABLE public.almanac_datasources
 DROP CONSTRAINT almanac_datasources_provider_check,
 DROP CONSTRAINT almanac_datasources_configuration_format_check,
 ADD CONSTRAINT almanac_datasources_provider_format_check CHECK (
  provider IN ('prometheus', 'victoriatraces', 'tempo', 'loki') AND
  configuration_format = provider || '-http-v1'
 );
