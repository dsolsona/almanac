// Focused static mock verification. Run through make shell, not application suites.
const {mkdirSync}=require('node:fs');
const {join}=require('node:path');
const {spawnSync}=require('node:child_process');
mkdirSync('/workspace/.local/alert-mock-review',{recursive:true});
for(const name of ['list','detail','creation']){
  const result=spawnSync(process.execPath,[join(__dirname,'checks',name+'.cjs')],{stdio:'inherit'});
  if(result.error)throw result.error;
  if(result.status!==0)process.exit(result.status||1);
}
